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

func TestFindTaskByID(t *testing.T) {
	s := &Store{Tasks: []Task{{ID: 1}, {ID: 7}}, NextID: 8}
	if _, err := s.findTaskByID(7); err != nil {
		t.Fatalf("want task 7, got err %v", err)
	}
	if _, err := s.findTaskByID(99); err == nil {
		t.Fatal("want error for missing id 99")
	}
}
