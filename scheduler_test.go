package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// schedNow is a fixed reference instant used across scheduler tests so firing is
// evaluated deterministically (never against the wall clock).
var schedNow = time.Date(2026, 1, 5, 10, 0, 30, 0, time.Local) // a Monday, 10:00:30

// dueTask builds a Daily task whose HH:MM is `offset` away from schedNow.
func dueTask(id int, offset time.Duration) Task {
	return Task{ID: id, Time: schedNow.Add(offset).Format("15:04"), Recurrence: Daily}
}

func TestTickFiresTaskInWindow(t *testing.T) {
	// Task time 10:00; now 10:00:30 → due 30s ago, inside [-2min, 0].
	task := dueTask(1, 0) // "10:00"
	store := &Store{Tasks: []Task{task}, NextID: 2}
	sc := newScheduler(store)

	sc.tickOnce(schedNow)

	if sc.firingID != 1 {
		t.Fatalf("firingID = %d, want 1", sc.firingID)
	}
	if !sc.fired[1] {
		t.Fatalf("fired[1] = false, want true")
	}
}

func TestTickIgnoresTaskOutsideWindow(t *testing.T) {
	// Task time 9:57; now 10:00:30 → due ~3.5min ago, outside the -2min window.
	task := dueTask(1, -3*time.Minute-time.Minute) // ~4 min earlier
	store := &Store{Tasks: []Task{task}, NextID: 2}
	sc := newScheduler(store)

	sc.tickOnce(schedNow)

	if sc.firingID != 0 {
		t.Fatalf("firingID = %d, want 0 (past window)", sc.firingID)
	}
	if sc.fired[1] {
		t.Fatalf("fired[1] = true, want false")
	}
}

func TestTickIgnoresFutureTask(t *testing.T) {
	task := dueTask(1, 30*time.Minute) // future
	store := &Store{Tasks: []Task{task}, NextID: 2}
	sc := newScheduler(store)

	sc.tickOnce(schedNow)

	if sc.firingID != 0 {
		t.Fatalf("firingID = %d, want 0 (future task)", sc.firingID)
	}
}

func TestTickClearsFiringWhenDone(t *testing.T) {
	task := dueTask(1, 0)
	store := &Store{Tasks: []Task{task}, NextID: 2}
	sc := newScheduler(store)

	sc.tickOnce(schedNow)
	if sc.firingID != 1 {
		t.Fatalf("precondition: firingID = %d, want 1", sc.firingID)
	}

	// Ack the alarm out-of-band (as POST /task/{id}/done would).
	store.mu.Lock()
	store.Tasks[0].Done = true
	store.mu.Unlock()

	sc.tickOnce(schedNow)
	if sc.firingID != 0 {
		t.Fatalf("firingID = %d, want 0 after task marked done", sc.firingID)
	}
}

func TestTickClearsFiringWhenSnoozed(t *testing.T) {
	// Firing task then snoozed → alarm clears, and fired flag is dropped so it
	// re-fires once the snooze lapses.
	task := dueTask(1, 0)
	store := &Store{Tasks: []Task{task}, NextID: 2}
	sc := newScheduler(store)

	sc.tickOnce(schedNow)
	if sc.firingID != 1 {
		t.Fatalf("precondition: firingID = %d, want 1", sc.firingID)
	}

	store.mu.Lock()
	store.Tasks[0].Snoozed = schedNow.Add(5 * time.Minute).Unix()
	store.mu.Unlock()

	sc.tickOnce(schedNow)
	if sc.firingID != 0 {
		t.Fatalf("firingID = %d, want 0 while snoozed", sc.firingID)
	}
	if sc.fired[1] {
		t.Fatalf("fired[1] = true, want false (cleared while snoozed)")
	}
}

func TestTickSnoozedTaskNotFiring(t *testing.T) {
	task := dueTask(1, 0)
	task.Snoozed = schedNow.Add(5 * time.Minute).Unix()
	store := &Store{Tasks: []Task{task}, NextID: 2}
	sc := newScheduler(store)
	sc.fired[1] = true // pretend it fired earlier today

	sc.tickOnce(schedNow)

	if sc.firingID != 0 {
		t.Fatalf("firingID = %d, want 0 (snoozed)", sc.firingID)
	}
	if sc.fired[1] {
		t.Fatalf("fired[1] = true, want cleared while snoozed")
	}
}

func TestStatusReportsFiring(t *testing.T) {
	task := dueTask(7, 0)
	store := &Store{Tasks: []Task{task}, NextID: 8}
	sc := newScheduler(store)

	sc.tickOnce(schedNow)
	if sc.firingID != 7 {
		t.Fatalf("precondition: firingID = %d, want 7", sc.firingID)
	}

	srv := httptest.NewServer(newDaemonMux(store, sc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var report StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode StatusReport: %v", err)
	}
	if !report.AlarmFiring {
		t.Fatalf("alarm_firing = false, want true")
	}
	if report.AlarmTask == nil || report.AlarmTask.ID != 7 {
		t.Fatalf("alarm_task = %+v, want task id 7", report.AlarmTask)
	}
}

func TestStatusClearsStaleFiring(t *testing.T) {
	// firingID points at a task that no longer exists → status reports no alarm
	// and clears the stale state.
	store := &Store{Tasks: []Task{}, NextID: 1}
	sc := newScheduler(store)
	sc.firingID = 42 // stale

	srv := httptest.NewServer(newDaemonMux(store, sc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var report StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.AlarmFiring {
		t.Fatalf("alarm_firing = true, want false for missing task")
	}
	if sc.firing() != 0 {
		t.Fatalf("stale firingID = %d, want cleared", sc.firing())
	}
}
