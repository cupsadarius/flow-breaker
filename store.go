package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── Data types & persistence ────────────────────────────────────────────────

type Recurrence string

const (
	Once     Recurrence = "once"
	Daily    Recurrence = "daily"
	Weekdays Recurrence = "weekdays"
	Weekly   Recurrence = "weekly"
	Monthly  Recurrence = "monthly"
	Custom   Recurrence = "custom"
)

type Task struct {
	ID         int             `json:"id"`
	Time       string          `json:"time"`
	Desc       string          `json:"desc"`
	Recurrence Recurrence      `json:"recurrence"`
	Days       []string        `json:"days,omitempty"`
	Done       bool            `json:"done"`
	Dismissed  bool            `json:"dismissed"`
	Snoozed    int64           `json:"snoozed"`
	Tags       []string        `json:"tags,omitempty"`
	CreatedAt  string          `json:"created_at"`
	History    map[string]bool `json:"history,omitempty"`
}

type Settings struct {
	SnoozeMins  int  `json:"snooze_mins"`
	AlertNotify bool `json:"alert_notify"`
	AlertDialog bool `json:"alert_dialog"`
	AlertSpeech bool `json:"alert_speech"`
	AlertSound  bool `json:"alert_sound"`
	AlertBell   bool `json:"alert_bell"`
	AlertTmux   bool `json:"alert_tmux"`
}

func defaultSettings() Settings {
	return Settings{
		SnoozeMins:  5,
		AlertNotify: true,
		AlertDialog: true,
		AlertSpeech: true,
		AlertSound:  true,
		AlertBell:   true,
		AlertTmux:   true,
	}
}

type Store struct {
	Tasks     []Task   `json:"tasks"`
	LastReset string   `json:"last_reset"`
	NextID    int      `json:"next_id"`
	Settings  Settings `json:"settings"`
}

func dataDir() string {
	dir := os.Getenv("FLOW_BREAKER_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".flow-breaker")
	}
	os.MkdirAll(dir, 0o755)
	return dir
}

func dataPath() string   { return filepath.Join(dataDir(), "tasks.json") }
func statusPath() string { return filepath.Join(dataDir(), "status.json") }
func sockPath() string   { return filepath.Join(dataDir(), "flow.sock") }

func loadStore() Store {
	var s Store
	data, err := os.ReadFile(dataPath())
	if err != nil {
		s.NextID = 1
		return s
	}
	json.Unmarshal(data, &s)
	if s.NextID == 0 {
		s.NextID = 1
	}
	if s.Settings.SnoozeMins == 0 {
		s.Settings = defaultSettings()
	}
	return s
}

func (s *Store) save() {
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(dataPath(), data, 0o644)
}

func (s *Store) addTask(timeStr, desc string, rec Recurrence, tags, days []string) Task {
	t := Task{
		ID:         s.NextID,
		Time:       timeStr,
		Desc:       desc,
		Recurrence: rec,
		Days:       days,
		Tags:       tags,
		CreatedAt:  time.Now().Format(time.RFC3339),
	}
	s.NextID++
	s.Tasks = append(s.Tasks, t)
	s.sortTasks()
	s.save()
	return t
}

func (s *Store) editTask(id int, timeStr, desc string, rec Recurrence, tags, days []string) {
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks[i].Time = timeStr
			s.Tasks[i].Desc = desc
			s.Tasks[i].Recurrence = rec
			s.Tasks[i].Tags = tags
			s.Tasks[i].Days = days
			break
		}
	}
	s.sortTasks()
	s.save()
}

func (s *Store) deleteTask(id int) {
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			break
		}
	}
	s.save()
}

func (s *Store) sortTasks() {
	sort.Slice(s.Tasks, func(i, j int) bool {
		return s.Tasks[i].Time < s.Tasks[j].Time
	})
}

func (s *Store) resetDaily() {
	today := time.Now().Format("2006-01-02")
	if s.LastReset == today {
		return
	}
	// record yesterday's completions in history before clearing
	for i := range s.Tasks {
		if s.Tasks[i].Done {
			if s.Tasks[i].History == nil {
				s.Tasks[i].History = make(map[string]bool)
			}
			s.Tasks[i].History[s.LastReset] = true
		}
	}
	for i := range s.Tasks {
		s.Tasks[i].Done = false
		s.Tasks[i].Dismissed = false
		s.Tasks[i].Snoozed = 0
	}
	s.LastReset = today
	s.save()
}

var weekdayShort = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func shouldFireToday(t Task) bool {
	return shouldFireOnDay(t, time.Now().Weekday())
}

func shouldFireOnDay(t Task, wd time.Weekday) bool {
	if len(t.Days) > 0 {
		today := weekdayShort[wd]
		for _, d := range t.Days {
			if d == today {
				return true
			}
		}
		return false
	}
	switch t.Recurrence {
	case Weekdays:
		return wd >= time.Monday && wd <= time.Friday
	default:
		return true
	}
}

func parseTaskTime(s string) (time.Time, bool) {
	now := time.Now()
	for _, f := range []string{"15:04", "3:04PM", "3:04 PM", "1504"} {
		t, err := time.Parse(f, strings.ToUpper(s))
		if err == nil {
			return time.Date(now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), 0, 0, now.Location()), true
		}
	}
	return time.Time{}, false
}

func timeUntil(t Task) time.Duration {
	target, ok := parseTaskTime(t.Time)
	if !ok {
		return 999 * time.Hour
	}
	return time.Until(target)
}

func fmtDuration(d time.Duration) string {
	neg := d < 0
	if neg {
		d = -d
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	sign := ""
	if neg {
		sign = "-"
	}
	if h > 0 {
		return fmt.Sprintf("%s%dh %02dm", sign, h, m)
	}
	return fmt.Sprintf("%s%dm %02ds", sign, m, s)
}
