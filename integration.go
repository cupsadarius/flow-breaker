package main

import (
	"fmt"
	"strings"
	"time"
)

// ── Status report (integration layer) ──────────────────────────────────────

// StatusReport is what external tools (Claude Code, scripts) consume
type StatusReport struct {
	Timestamp      string          `json:"timestamp"`
	AlarmFiring    bool            `json:"alarm_firing"`
	AlarmTask      *Task           `json:"alarm_task,omitempty"`
	Next           *TaskStatus     `json:"next,omitempty"`
	Overdue        []TaskStatus    `json:"overdue"`
	Upcoming       []TaskStatus    `json:"upcoming"`
	Done           []TaskStatus    `json:"done"`
	Nudge          string          `json:"nudge"`
	CalendarEvents []CalendarEvent `json:"calendar_events,omitempty"`
}

type TaskStatus struct {
	Task
	TimeUntil string `json:"time_until"`
	Seconds   int    `json:"seconds"`
}

// buildStatus builds a StatusReport, taking the store read lock itself.
func buildStatus(store *Store, alarm *alarmState) StatusReport {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return buildStatusLocked(store, alarm)
}

// buildStatusLocked does the real work and assumes the caller already holds
// store.mu for reading. Callers that need to resolve an alarm's task index and
// build the report atomically (e.g. GET /status) use this under a single lock.
func buildStatusLocked(store *Store, alarm *alarmState) StatusReport {
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
		if t.Archived || !shouldFireToday(t) {
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
