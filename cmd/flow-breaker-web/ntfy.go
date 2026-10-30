package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ntfyAction struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ntfyMessage is one line of ntfy's JSON stream (GET /{topic}/json).
type ntfyMessage struct {
	ID       string       `json:"id"`
	Time     int64        `json:"time"`
	Event    string       `json:"event"`
	Message  string       `json:"message"`
	Priority int          `json:"priority"`
	Actions  []ntfyAction `json:"actions"`
}

type alarm struct {
	TaskID   int
	Time     string
	Desc     string
	Priority int
}

var doneURL = regexp.MustCompile(`/task/(\d+)/done$`)

// parseAlarm reads a flow-breaker escalation message: the task ID from the
// Done action URL, "HH:MM — desc" from the body, priority 3 when unset.
func parseAlarm(msg ntfyMessage) (alarm, bool) {
	var a alarm
	for _, act := range msg.Actions {
		if u, err := url.Parse(act.URL); err == nil && act.Label == "Done" {
			if m := doneURL.FindStringSubmatch(u.Path); m != nil {
				a.TaskID, _ = strconv.Atoi(m[1])
			}
		}
	}
	if a.TaskID == 0 {
		return a, false
	}
	if at, desc, ok := strings.Cut(msg.Message, " — "); ok {
		a.Time, a.Desc = at, desc
	} else {
		a.Desc = msg.Message
	}
	a.Priority = msg.Priority
	if a.Priority == 0 {
		a.Priority = 3
	}
	return a, true
}

func alarmPayload(a alarm) []byte {
	body := a.Desc
	if a.Time != "" {
		body = a.Time + " — " + a.Desc
	}
	b, _ := json.Marshal(struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		TaskID   int    `json:"taskId"`
		Tag      string `json:"tag"`
		Priority int    `json:"priority"`
	}{"flow-breaker", body, a.TaskID, fmt.Sprintf("task-%d", a.TaskID), a.Priority})
	return b
}

// ntfySub keeps one subscription to the Pi's ntfy topic and pushes every
// fresh alarm to the phone.
type ntfySub struct {
	base, topic, token string
	push               func([]byte) error
	lastID             string
}

const alarmFreshness = 90 * time.Second

func (s *ntfySub) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		opened, err := s.stream(ctx)
		if opened {
			backoff = time.Second
		}
		log.Printf("ntfy stream: %v (retry in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *ntfySub) stream(ctx context.Context) (opened bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	since := "2m"
	if s.lastID != "" {
		since = s.lastID
	}
	u := fmt.Sprintf("%s/%s/json?since=%s", strings.TrimRight(s.base, "/"), url.PathEscape(s.topic), url.QueryEscape(since))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	// ntfy sends a keepalive every 45 s; silence for 90 s means a dead stream.
	idle := time.AfterFunc(alarmFreshness, cancel)
	defer idle.Stop()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		idle.Reset(alarmFreshness)
		var msg ntfyMessage
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.Event != "message" {
			continue
		}
		s.lastID = msg.ID
		a, ok := parseAlarm(msg)
		if !ok || time.Since(time.Unix(msg.Time, 0)) > alarmFreshness {
			continue // not an alarm, or a replay of one that already ended
		}
		if err := s.push(alarmPayload(a)); err != nil {
			log.Printf("push: %v", err)
		}
	}
	if err := sc.Err(); err != nil {
		return true, err
	}
	return true, io.EOF
}
