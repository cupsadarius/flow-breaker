package main

import (
	"os"
	"testing"
	"time"
)

// TestSaveAtomic guards the durable-write path: save() must produce a tasks.json
// that round-trips via loadStore, and must leave no leftover *.tmp file behind
// (a mid-write crash must never corrupt the sole durable copy).
func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOW_BREAKER_DIR", dir)

	s := &Store{NextID: 1, Settings: defaultSettings()}
	s.addTaskSource("09:00", "", "standup", Daily, nil, nil, "cli")

	// tasks.json exists and round-trips.
	if _, err := os.Stat(dataPath()); err != nil {
		t.Fatalf("tasks.json not written: %v", err)
	}
	got := loadStore()
	if len(got.Tasks) != 1 || got.Tasks[0].Desc != "standup" {
		t.Fatalf("round-trip failed: %+v", got.Tasks)
	}

	// No leftover temp files in the data dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "tasks.json" {
			continue
		}
		t.Fatalf("unexpected leftover file in data dir: %s", e.Name())
	}
}

// TestLoadStoreDefaultsOnEmptyDir guards the fresh-deploy path: with no
// tasks.json present, loadStore must still return working default settings
// (not the zero value with all alerts off and SnoozeMins:0).
func TestLoadStoreDefaultsOnEmptyDir(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	s := loadStore()
	if s.NextID != 1 {
		t.Fatalf("NextID = %d, want 1", s.NextID)
	}
	if s.Settings != defaultSettings() {
		t.Fatalf("Settings = %+v, want defaults %+v", s.Settings, defaultSettings())
	}
	if s.Settings.SnoozeMins != 5 || !s.Settings.AlertNotify || s.Settings.CalCacheMins != 15 {
		t.Fatalf("defaults not applied: %+v", s.Settings)
	}
}

// TestInboxImmortalAcrossReset guards Task 1: an inbox item is never reset or
// archived — it survives resetDaily with every field intact.
func TestInboxImmortalAcrossReset(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	s := &Store{NextID: 1, Settings: defaultSettings()}
	item := s.addInbox("call the plumber", "phone")
	if item.ID == 0 || item.CreatedAt == "" {
		t.Fatalf("addInbox did not stamp id/createdAt: %+v", item)
	}

	s.resetDaily()

	if len(s.Inbox) != 1 {
		t.Fatalf("inbox len = %d, want 1 (immortal)", len(s.Inbox))
	}
	got := s.Inbox[0]
	if got.ID != item.ID || got.Desc != "call the plumber" || got.Source != "phone" {
		t.Fatalf("inbox item mutated by reset: %+v", got)
	}
	if got.Archived {
		t.Fatal("inbox item must never be archived")
	}
}

// TestDateGateFiring guards Task 2: a task pinned to a future Date does not fire
// on any other day, but does fire on its Date. The gate lives in
// shouldFireTodayAt so scheduler and status agree.
func TestDateGateFiring(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.Local)
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	task := Task{Time: "09:00", Date: tomorrow, Recurrence: Once}

	if shouldFireTodayAt(task, now) {
		t.Fatal("dated-tomorrow task must NOT fire today")
	}
	if !shouldFireTodayAt(task, now.AddDate(0, 0, 1)) {
		t.Fatal("dated task must fire on its Date")
	}
}

// TestRecurringCapturesFireBeyondCreationDay guards C1: a recurring capture
// carries NO Date and must keep firing on later matching days, not just its
// creation day. Defense-in-depth: even a stray Date on a non-Once task is
// ignored by the firing gate (scoped to Once), so it can never zombie.
func TestRecurringCapturesFireBeyondCreationDay(t *testing.T) {
	// parseNow's Monday is 2026-01-05; a week later is also a Monday.
	laterMonday := time.Date(2026, 1, 12, 9, 0, 0, 0, time.Local)
	nextDay := time.Date(2026, 1, 6, 8, 30, 0, 0, time.Local)

	weekdays := Task{Time: "09:00", Recurrence: Weekdays} // Date "" as the parser now emits
	if !shouldFireTodayAt(weekdays, laterMonday) {
		t.Fatal("undated Weekdays capture must fire on a later Monday, not just creation day")
	}

	daily := Task{Time: "08:30", Recurrence: Daily}
	if !shouldFireTodayAt(daily, nextDay) {
		t.Fatal("undated Daily capture must fire the next day")
	}

	// A stale Date on a recurring task must NOT pin it (Once-scoped gate).
	staleDated := Task{Time: "09:00", Date: "2026-01-05", Recurrence: Weekdays}
	if !shouldFireTodayAt(staleDated, laterMonday) {
		t.Fatal("stray Date on a recurring task must be ignored by the firing gate")
	}
}

// TestResetArchivesDatedOnceCorrectly guards Task 2's archive gate: a dated
// once-task is not archived by a reset run on its Date (it survives until the
// day after), while an undated once-task created before today archives as
// before.
func TestResetArchivesDatedOnceCorrectly(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format(time.RFC3339)

	s := &Store{NextID: 10, Settings: defaultSettings()}
	// Dated once-task whose Date is today, though created yesterday — must NOT
	// be archived by a reset "on its date".
	s.Tasks = append(s.Tasks, Task{ID: 1, Time: "09:00", Date: today, Recurrence: Once, CreatedAt: yesterday})
	// Undated once-task created yesterday — archives as before.
	s.Tasks = append(s.Tasks, Task{ID: 2, Time: "10:00", Recurrence: Once, CreatedAt: yesterday})

	s.resetDaily()

	dated, _ := s.findTaskByID(1)
	undated, _ := s.findTaskByID(2)
	if s.Tasks[dated].Archived {
		t.Fatal("dated once-task must survive a reset run on its Date")
	}
	if !s.Tasks[undated].Archived {
		t.Fatal("undated once-task created yesterday must archive")
	}
}

// calEvent is a tiny helper for building an eligible timed CalendarEvent.
func calEvent(uid, summary, start string) CalendarEvent {
	return CalendarEvent{UID: uid, Summary: summary, StartTime: start}
}

// TestMaterializeCalendarNewEvent guards Task 2: a new eligible event becomes a
// single Once task at start−10m, Source "calendar", CalUID set. The alarm-time
// underflow case (00:05 → previous day 23:55) is checked too.
func TestMaterializeCalendarNewEvent(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	added, updated, dropped := s.materializeCalendar([]CalendarEvent{calEvent("u1", "Standup", "09:00")}, now)
	if added != 1 || updated != 0 || dropped != 0 {
		t.Fatalf("counts = (%d,%d,%d), want (1,0,0)", added, updated, dropped)
	}
	if len(s.Tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(s.Tasks))
	}
	got := s.Tasks[0]
	if got.Time != "08:50" || got.Date != "2026-08-14" {
		t.Fatalf("alarm time/date = %q/%q, want 08:50/2026-08-14", got.Time, got.Date)
	}
	if got.Source != "calendar" || got.CalUID != "u1" || got.Recurrence != Once {
		t.Fatalf("bad materialized task: %+v", got)
	}

	// Cross-midnight clamp: a 00:05 event's 10-min lead would land on the
	// previous day (23:55) as instantly-missed, so it clamps to event time on
	// now's date instead.
	ts, ds, ok := calendarAlarmTime("00:05", now)
	if !ok || ts != "00:05" || ds != "2026-08-14" {
		t.Fatalf("clamped alarm = %q/%q ok=%v, want 00:05/2026-08-14", ts, ds, ok)
	}
	// A normal event well after midnight keeps its full 10-min lead.
	ts, ds, ok = calendarAlarmTime("00:20", now)
	if !ok || ts != "00:10" || ds != "2026-08-14" {
		t.Fatalf("00:20 lead = %q/%q ok=%v, want 00:10/2026-08-14", ts, ds, ok)
	}
}

// TestMaterializeCalendarUpsertPreservesAck guards Task 2's upsert: the same UID
// with a moved start updates Time in place (still one task) while preserving the
// task's Done/Snoozed ack state.
func TestMaterializeCalendarUpsertPreservesAck(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	s.materializeCalendar([]CalendarEvent{calEvent("u1", "Standup", "09:00")}, now)
	// Simulate ack state on the materialized task.
	s.Tasks[0].Snoozed = 12345
	s.Tasks[0].Dismissed = true
	s.Tasks[0].Missed = true
	id := s.Tasks[0].ID

	added, updated, dropped := s.materializeCalendar([]CalendarEvent{calEvent("u1", "Standup", "10:00")}, now)
	if added != 0 || updated != 1 || dropped != 0 {
		t.Fatalf("counts = (%d,%d,%d), want (0,1,0)", added, updated, dropped)
	}
	if len(s.Tasks) != 1 {
		t.Fatalf("upsert must not duplicate: got %d tasks", len(s.Tasks))
	}
	got := s.Tasks[0]
	if got.ID != id {
		t.Fatalf("upsert must reuse the same task id: was %d now %d", id, got.ID)
	}
	if got.Time != "09:50" {
		t.Fatalf("moved-time upsert Time = %q, want 09:50", got.Time)
	}
	if got.Snoozed != 12345 || !got.Dismissed || !got.Missed {
		t.Fatalf("upsert must preserve Snoozed/Dismissed/Missed ack state, got %+v", got)
	}
}

// TestMaterializeCalendarNoOpDoesNotRewrite guards the SD-card-wear fix: a second
// identical refresh (nothing added/updated/dropped) must not rewrite tasks.json.
// We prove no write happened by rewinding the file's mtime and asserting it is
// unchanged after the no-op call (saveLocked would bump it to ~now).
func TestMaterializeCalendarNoOpDoesNotRewrite(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	events := []CalendarEvent{calEvent("u1", "Standup", "09:00")}
	s.materializeCalendar(events, now) // first call writes the file

	// Rewind mtime well into the past so a rewrite is unambiguously detectable.
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(dataPath(), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	added, updated, dropped := s.materializeCalendar(events, now)
	if added != 0 || updated != 0 || dropped != 0 {
		t.Fatalf("second identical refresh counts = (%d,%d,%d), want (0,0,0)", added, updated, dropped)
	}
	fi, err := os.Stat(dataPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.ModTime().Equal(old) {
		t.Fatalf("no-op refresh rewrote tasks.json: mtime moved from %v to %v", old, fi.ModTime())
	}
}

// TestMaterializeCalendarSkipsIneligible guards Task 2: declined, all-day, and
// no-start events are never materialized.
func TestMaterializeCalendarSkipsIneligible(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	events := []CalendarEvent{
		{UID: "d1", Summary: "Declined", StartTime: "09:00", Declined: true},
		{UID: "a1", Summary: "All day", AllDay: true},
		{UID: "n1", Summary: "No start"},
	}
	added, updated, dropped := s.materializeCalendar(events, now)
	if added != 0 || updated != 0 || dropped != 0 {
		t.Fatalf("counts = (%d,%d,%d), want (0,0,0)", added, updated, dropped)
	}
	if len(s.Tasks) != 0 {
		t.Fatalf("ineligible events must not materialize, got %d tasks", len(s.Tasks))
	}
}

// TestMaterializeCalendarReconcileVanished guards Task 2's reconcile: an un-acted
// calendar task whose UID vanished from the feed is dropped; a Done one is kept.
func TestMaterializeCalendarReconcileVanished(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	// Seed two materialized calendar tasks.
	s.materializeCalendar([]CalendarEvent{
		calEvent("keep-done", "Kept", "09:00"),
		calEvent("drop-me", "Dropped", "10:00"),
	}, now)
	// Ack the first one.
	for i := range s.Tasks {
		if s.Tasks[i].CalUID == "keep-done" {
			s.Tasks[i].Done = true
		}
	}

	// Feed now contains neither UID.
	added, updated, dropped := s.materializeCalendar(nil, now)
	if added != 0 || updated != 0 || dropped != 1 {
		t.Fatalf("counts = (%d,%d,%d), want (0,0,1)", added, updated, dropped)
	}
	if len(s.Tasks) != 1 {
		t.Fatalf("want 1 surviving task (the Done tombstone), got %d", len(s.Tasks))
	}
	if s.Tasks[0].CalUID != "keep-done" || !s.Tasks[0].Done {
		t.Fatalf("Done tombstone must survive vanish: %+v", s.Tasks[0])
	}
}

// TestMaterializeCalendarDoneTombstone guards Task 2: a Done calendar task whose
// UID is still in the feed stays Done and is not re-surfaced (no new task, ack
// preserved).
func TestMaterializeCalendarDoneTombstone(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	s.materializeCalendar([]CalendarEvent{calEvent("u1", "Standup", "09:00")}, now)
	s.Tasks[0].Done = true

	added, _, dropped := s.materializeCalendar([]CalendarEvent{calEvent("u1", "Standup", "09:00")}, now)
	if added != 0 || dropped != 0 {
		t.Fatalf("re-materialize of a Done event must not add/drop: (%d,_,%d)", added, dropped)
	}
	if len(s.Tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(s.Tasks))
	}
	if !s.Tasks[0].Done {
		t.Fatal("Done calendar task must stay Done when its UID is re-materialized")
	}
}

// TestResetDailyTreatsCalendarLikeAny guards Task 2: calendar tasks need no
// special-casing in resetDaily — an un-acted dated once calendar task clears its
// per-day flags like any task.
func TestResetDailyTreatsCalendarLikeAny(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Now()
	today := now.Format("2006-01-02")
	s := &Store{NextID: 1, Settings: defaultSettings(), LastReset: now.AddDate(0, 0, -1).Format("2006-01-02")}
	s.Tasks = append(s.Tasks, Task{
		ID: 1, Time: "08:50", Date: today, Desc: "Standup", Recurrence: Once,
		Source: "calendar", CalUID: "u1", Snoozed: 999, Missed: true,
		CreatedAt: now.Format(time.RFC3339),
	})

	s.resetDaily()

	got := s.Tasks[0]
	if got.Archived {
		t.Fatal("a calendar task dated today must not archive on reset")
	}
	if got.Snoozed != 0 || got.Missed {
		t.Fatalf("reset must clear per-day flags on a calendar task: %+v", got)
	}
}

// TestMaterializeCalendarSpareNonCalendarTasks guards the reconcile scope: only
// Source "calendar" tasks are eligible for the vanished-drop. A regular task
// absent from the feed must never be deleted.
func TestMaterializeCalendarSpareNonCalendarTasks(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}
	s.addTaskSource("07:00", "", "manual task", Once, nil, nil, "tui")

	// Feed is empty — nothing calendar exists, so nothing to drop.
	added, updated, dropped := s.materializeCalendar(nil, now)
	if added != 0 || updated != 0 || dropped != 0 {
		t.Fatalf("counts = (%d,%d,%d), want (0,0,0)", added, updated, dropped)
	}
	if len(s.Tasks) != 1 || s.Tasks[0].Source != "tui" {
		t.Fatalf("non-calendar task must survive reconcile: %+v", s.Tasks)
	}
}

// TestMaterializeCalendarOutOfOrderRematch guards the match-by-value invariant:
// upsert keys on CalUID (a value), not a stored index, so a later batch whose
// events sort into a different order still re-matches existing tasks by UID
// after sortTasks reorders — no duplicates, no cross-wiring.
func TestMaterializeCalendarOutOfOrderRematch(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local)
	s := &Store{NextID: 1, Settings: defaultSettings()}

	// First batch: uA at 09:00, uB at 15:00 (sorted A before B by Time).
	s.materializeCalendar([]CalendarEvent{
		calEvent("uA", "Alpha", "09:00"),
		calEvent("uB", "Bravo", "15:00"),
	}, now)
	if len(s.Tasks) != 2 {
		t.Fatalf("want 2 tasks after first batch, got %d", len(s.Tasks))
	}

	// Second batch, out of order and with swapped-around times that force a
	// re-sort: uB now at 08:00 (sorts first), uA now at 20:00 (sorts last).
	added, updated, dropped := s.materializeCalendar([]CalendarEvent{
		calEvent("uB", "Bravo", "08:00"),
		calEvent("uA", "Alpha", "20:00"),
	}, now)
	if added != 0 || dropped != 0 {
		t.Fatalf("re-match must not add or drop: added=%d dropped=%d", added, dropped)
	}
	if updated != 2 {
		t.Fatalf("both tasks moved time, want updated=2, got %d", updated)
	}
	if len(s.Tasks) != 2 {
		t.Fatalf("re-match must not duplicate: got %d tasks", len(s.Tasks))
	}
	byUID := map[string]Task{}
	for _, tk := range s.Tasks {
		byUID[tk.CalUID] = tk
	}
	if byUID["uB"].Time != "07:50" || byUID["uB"].Desc != "Bravo" {
		t.Fatalf("uB mis-wired after re-sort: %+v", byUID["uB"])
	}
	if byUID["uA"].Time != "19:50" || byUID["uA"].Desc != "Alpha" {
		t.Fatalf("uA mis-wired after re-sort: %+v", byUID["uA"])
	}
}

func TestFindTaskByID(t *testing.T) {
	s := &Store{Tasks: []Task{{ID: 1}, {ID: 7}}, NextID: 8}
	if _, err := s.findTaskByID(7); err != nil {
		t.Fatalf("want task 7, got err %v", err)
	}
	if _, err := s.findTaskByID(99); err == nil {
		t.Fatal("want error for missing id 99")
	}
}
