package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func press(t *testing.T, m model, k tea.KeyMsg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(k)
	return next.(model), cmd
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// midDay skips tests whose "overdue" and "upcoming" fixtures would wrap past midnight.
func midDay(t *testing.T) time.Time {
	t.Helper()
	now := time.Now()
	if now.Hour() < 2 || now.Hour() > 21 {
		t.Skip("needs a mid-day clock")
	}
	return now
}

func TestTabCyclesFocus(t *testing.T) {
	m := model{store: &Store{}, width: 120, height: 40}
	var want = []pane{paneHabits, paneInbox, paneToday}
	for _, w := range want {
		m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if m.focus != w {
			t.Fatalf("focus = %d, want %d", m.focus, w)
		}
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.focus != paneInbox {
		t.Fatalf("shift+tab focus = %d, want inbox", m.focus)
	}
}

func TestOfflineBlocksDone(t *testing.T) {
	now := midDay(t)
	late := now.Add(-time.Hour).Format("15:04")
	m := model{store: &Store{Tasks: []Task{{ID: 1, Time: late, Desc: "stretch", Recurrence: Daily}}}, offline: true, width: 120, height: 40}
	m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected no command while offline")
	}
	if m.msg != "daemon down" {
		t.Fatalf("msg = %q, want daemon down", m.msg)
	}
}

func TestEmptyStoreRenders(t *testing.T) {
	m := model{store: &Store{}, width: 120, height: 40}
	for _, k := range []tea.KeyMsg{runes("j"), runes("k"), {Type: tea.KeyEnter}, {Type: tea.KeyTab}, runes("j"), {Type: tea.KeyEnter}, {Type: tea.KeyTab}, runes("k")} {
		m, _ = press(t, m, k)
	}
	v := m.View()
	for _, s := range []string{"No tasks today", "No habits yet", "Inbox empty"} {
		if !strings.Contains(v, s) {
			t.Fatalf("view missing %q", s)
		}
	}
}

func TestViewShowsGroups(t *testing.T) {
	now := midDay(t)
	m := model{store: &Store{Tasks: []Task{
		{ID: 1, Time: now.Add(-time.Hour).Format("15:04"), Desc: "stretch", Recurrence: Daily},
		{ID: 2, Time: now.Add(time.Hour).Format("15:04"), Desc: "squats", Recurrence: Daily},
	}}, width: 120, height: 40}
	v := m.View()
	if !strings.Contains(v, "OVERDUE") || !strings.Contains(v, "NEXT") || strings.Contains(v, "LATER") {
		t.Fatalf("view groups wrong:\n%s", v)
	}
}
