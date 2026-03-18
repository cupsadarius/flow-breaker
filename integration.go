package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// ── Status file + Unix socket (integration layer) ──────────────────────────

// StatusReport is what external tools (Claude Code, scripts) consume
type StatusReport struct {
	Timestamp   string       `json:"timestamp"`
	AlarmFiring bool         `json:"alarm_firing"`
	AlarmTask   *Task        `json:"alarm_task,omitempty"`
	Next        *TaskStatus  `json:"next,omitempty"`
	Overdue     []TaskStatus `json:"overdue"`
	Upcoming    []TaskStatus `json:"upcoming"`
	Done        []TaskStatus `json:"done"`
	Nudge       string       `json:"nudge"`
}

type TaskStatus struct {
	Task
	TimeUntil string `json:"time_until"`
	Seconds   int    `json:"seconds"`
}

func buildStatus(store *Store, alarm *alarmState) StatusReport {
	now := time.Now()
	r := StatusReport{
		Timestamp: now.Format(time.RFC3339),
	}

	if alarm != nil && alarm.active && alarm.taskIdx >= 0 && alarm.taskIdx < len(store.Tasks) {
		r.AlarmFiring = true
		t := store.Tasks[alarm.taskIdx]
		r.AlarmTask = &t
		r.Nudge = fmt.Sprintf("🚨 ALARM: %s — %s (do it NOW)", t.Time, t.Desc)
	}

	for _, t := range store.Tasks {
		if !shouldFireToday(t) {
			continue
		}
		d := timeUntil(t)
		secs := int(d.Seconds())
		ts := TaskStatus{Task: t, TimeUntil: fmtDuration(d), Seconds: secs}

		if t.Done {
			r.Done = append(r.Done, ts)
		} else if d < 0 && !t.Dismissed {
			r.Overdue = append(r.Overdue, ts)
		} else if !t.Dismissed {
			r.Upcoming = append(r.Upcoming, ts)
		}
	}

	// pick next
	if len(r.Upcoming) > 0 {
		r.Next = &r.Upcoming[0]
	}

	// build nudge if no alarm
	if !r.AlarmFiring {
		if len(r.Overdue) > 0 {
			descs := make([]string, len(r.Overdue))
			for i, o := range r.Overdue {
				descs[i] = fmt.Sprintf("%s (%s)", o.Desc, o.Time)
			}
			r.Nudge = fmt.Sprintf("⚠️ OVERDUE: %s", strings.Join(descs, ", "))
		} else if r.Next != nil && r.Next.Seconds < 600 {
			r.Nudge = fmt.Sprintf("⏰ In %s: %s — wrap up what you're doing",
				fmtDuration(timeUntil(r.Next.Task)), r.Next.Desc)
		} else if r.Next != nil {
			r.Nudge = fmt.Sprintf("Next: %s %s (%s away)",
				r.Next.Time, r.Next.Desc, fmtDuration(timeUntil(r.Next.Task)))
		} else {
			r.Nudge = "✅ All clear — no more tasks today"
		}
	}

	return r
}

func writeStatusFile(store *Store, alarm *alarmState) {
	report := buildStatus(store, alarm)
	data, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(statusPath(), data, 0o644)
}

func startSocketServer(store *Store, alarm *alarmState) {
	path := sockPath()
	os.Remove(path) // clean up stale socket

	ln, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	os.Chmod(path, 0o666)

	go func() {
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				continue
			}
			go handleConn(conn, store, alarm)
		}
	}()
}

func handleConn(conn net.Conn, store *Store, alarm *alarmState) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	cmd := strings.TrimSpace(string(buf[:n]))

	switch cmd {
	case "status":
		report := buildStatus(store, alarm)
		data, _ := json.MarshalIndent(report, "", "  ")
		conn.Write(data)
	case "nudge":
		report := buildStatus(store, alarm)
		conn.Write([]byte(report.Nudge + "\n"))
	case "next":
		report := buildStatus(store, alarm)
		if report.Next != nil {
			data, _ := json.Marshal(report.Next)
			conn.Write(data)
		} else {
			conn.Write([]byte(`{"nudge":"all clear"}`))
		}
	case "overdue":
		report := buildStatus(store, alarm)
		data, _ := json.Marshal(report.Overdue)
		conn.Write(data)
	case "alarm":
		report := buildStatus(store, alarm)
		if report.AlarmFiring {
			data, _ := json.Marshal(report.AlarmTask)
			conn.Write(data)
		} else {
			conn.Write([]byte("false\n"))
		}
	default:
		conn.Write([]byte(`{"error":"unknown command","commands":["status","nudge","next","overdue","alarm"]}` + "\n"))
	}
}
