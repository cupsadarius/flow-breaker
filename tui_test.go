package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestApplySyncErrorGoesOffline verifies a failed daemon refresh flips the model
// to offline while retaining the last-known cache and report (fail loud, never a
// false empty/all-clear).
func TestApplySyncErrorGoesOffline(t *testing.T) {
	prevReport := &StatusReport{Nudge: "old"}
	m := &model{
		store:  &Store{Tasks: []Task{{ID: 1, Desc: "keep"}}},
		report: prevReport,
	}

	if cmd := m.applySync(syncMsg{err: ErrDaemonDown}); cmd != nil {
		t.Fatalf("expected no command on error, got one")
	}

	if !m.offline {
		t.Fatalf("offline = false, want true after fetch error")
	}
	if len(m.store.Tasks) != 1 || m.store.Tasks[0].Desc != "keep" {
		t.Fatalf("cache not retained: %+v", m.store.Tasks)
	}
	if m.report != prevReport {
		t.Fatalf("report not retained on error")
	}
}

// TestApplySyncSuccessUpdatesCache verifies a successful refresh clears offline,
// replaces the cache + report, and stamps lastSync.
func TestApplySyncSuccessUpdatesCache(t *testing.T) {
	m := &model{store: &Store{}, offline: true}
	newTasks := []Task{{ID: 2, Desc: "new"}}
	rep := &StatusReport{Nudge: "fresh"}

	// Recovering from offline should schedule a settings refetch.
	cmd := m.applySync(syncMsg{tasks: newTasks, report: rep})
	if cmd == nil {
		t.Fatalf("expected settings-refetch command on offline→online recovery")
	}

	if m.offline {
		t.Fatalf("offline = true, want false after successful sync")
	}
	if len(m.store.Tasks) != 1 || m.store.Tasks[0].Desc != "new" {
		t.Fatalf("cache not updated: %+v", m.store.Tasks)
	}
	if m.report != rep {
		t.Fatalf("report not updated")
	}
	if m.lastSync.IsZero() {
		t.Fatalf("lastSync not stamped")
	}
}

// TestApplySyncClampsCursor verifies a shrunk task list pulls the cursor back
// in-bounds so the highlight can't point past the end.
func TestApplySyncClampsCursor(t *testing.T) {
	m := &model{
		store:  &Store{Tasks: []Task{{ID: 1}, {ID: 2}, {ID: 3}}},
		cursor: 2,
	}
	m.applySync(syncMsg{tasks: []Task{{ID: 1}}, report: &StatusReport{}})
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after list shrank to 1", m.cursor)
	}
}

// TestSyncAlertEdges verifies local alerts fire once per firing alarm (not on
// every sync) and the pane status clears when the alarm stops.
func TestSyncAlertEdges(t *testing.T) {
	var fired []string
	cleared := 0
	alertFn = func(_, body string, _ *Settings) { fired = append(fired, body) }
	clearAlertFn = func() { cleared++ }
	t.Cleanup(func() { alertFn, clearAlertFn = alertAll, clearPaneAlert })

	firing := func(id int, desc string) *StatusReport {
		return &StatusReport{AlarmFiring: true, AlarmTask: &Task{ID: id, Desc: desc}}
	}
	m := &model{store: &Store{}}
	m.applySync(syncMsg{report: firing(1, "stretch")})
	m.applySync(syncMsg{report: firing(1, "stretch")})
	m.applySync(syncMsg{report: firing(2, "water")})
	m.applySync(syncMsg{report: &StatusReport{}})
	m.applySync(syncMsg{report: &StatusReport{}})
	m.applySync(syncMsg{report: firing(1, "stretch")}) // re-fire after snooze

	if want := []string{"stretch", "water", "stretch"}; strings.Join(fired, ",") != strings.Join(want, ",") {
		t.Fatalf("fired = %v, want %v", fired, want)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
}

func TestOscText(t *testing.T) {
	if got := oscText("a;b\x1b]0;x\x07\nc"); got != "a b ]0 x  c" {
		t.Fatalf("oscText = %q", got)
	}
}

func TestGroupToday(t *testing.T) {
	now := time.Date(2026, 10, 30, 13, 30, 0, 0, time.Local) // Friday
	tasks := []Task{
		{ID: 1, Time: "16:30", Recurrence: Daily},
		{ID: 2, Time: "13:00", Recurrence: Daily},
		{ID: 3, Time: "10:00", Recurrence: Daily, Done: true},
		{ID: 4, Time: "15:00", Recurrence: Daily},
		{ID: 5, Time: "11:30", Recurrence: Daily, Dismissed: true},
		{ID: 6, Time: "12:00", Recurrence: Custom, Days: []string{"sat", "sun"}},
	}
	g := groupToday(tasks, now)
	ids := func(ts []Task) []int {
		var out []int
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	if got := fmt.Sprint(ids(g.Overdue), ids(g.Next), ids(g.Later), ids(g.Done)); got != "[2] [4] [1] [3 5]" {
		t.Fatalf("groups = %s, want [2] [4] [1] [3 5]", got)
	}
}
