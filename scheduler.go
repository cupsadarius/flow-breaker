package main

import (
	"sync"
	"time"
)

// scheduler owns the alarm-firing state in the daemon. It re-evaluates firing
// once per second and tracks the currently firing task by its stable ID — never
// a slice index, since concurrent edits call sortTasks() and reorder s.Tasks, so
// a stored index would go stale.
//
// This phase maintains firing STATE only; emitting the actual alert (sound,
// ntfy, macOS, tmux) is Phase 6.
type scheduler struct {
	store *Store

	mu        sync.Mutex   // guards firingID, tick, fired, firedDate
	firingID  int          // 0 = no alarm firing
	tick      int          // Phase 6 ladder counter (write-only this phase)
	fired     map[int]bool // task ID -> already fired today
	firedDate string       // day the fired map applies to ("2006-01-02")
}

func newScheduler(store *Store) *scheduler {
	return &scheduler{store: store, fired: map[int]bool{}}
}

// run ticks once per second forever (the daemon lives for the process
// lifetime). Tests drive tickOnce directly with an injected clock instead.
func (sc *scheduler) run() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		sc.tickOnce(time.Now())
	}
}

// firing returns the ID of the currently firing task, or 0 if none.
func (sc *scheduler) firing() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.firingID
}

// clearFiring clears the firing state, but only if it still points at id (so a
// concurrent tick that already moved on is not clobbered). Used by the /status
// handler when a firing task can no longer be resolved (e.g. it was deleted).
func (sc *scheduler) clearFiring(id int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.firingID == id {
		sc.firingID = 0
		sc.tick = 0
	}
}

// tickOnce re-evaluates firing state for the given instant. It snapshots the
// tasks under the store read lock, releases it, then updates scheduler state
// under sc.mu — the two locks are never held nested.
func (sc *scheduler) tickOnce(now time.Time) {
	sc.store.mu.RLock()
	tasks := make([]Task, len(sc.store.Tasks))
	copy(tasks, sc.store.Tasks)
	sc.store.mu.RUnlock()

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Day rollover: a long-lived daemon must forget yesterday's fired flags so
	// Daily/Weekdays tasks fire again today.
	if day := now.Format("2006-01-02"); day != sc.firedDate {
		sc.fired = map[int]bool{}
		sc.firedDate = day
	}

	// A currently-snoozed task clears its fired flag so it re-fires once the
	// snooze lapses (matches v1).
	for _, t := range tasks {
		if t.Snoozed > 0 && now.Unix() < t.Snoozed {
			delete(sc.fired, t.ID)
		}
	}

	// If a task is firing, keep it firing until it no longer meets the window
	// (done/dismissed/snoozed/out-of-window). This is how an HTTP done/snooze
	// clears the alarm — no direct coupling to the handlers is needed.
	if sc.firingID != 0 {
		if t, ok := taskByID(tasks, sc.firingID); ok && meetsFireWindow(t, now) {
			sc.tick++
			return
		}
		sc.firingID = 0
		sc.tick = 0
	}

	// Nothing firing: fire the first eligible, not-yet-fired task. Single active
	// alarm this phase (Phase 6 makes alarms per-task independent ladders).
	for _, t := range tasks {
		if sc.fired[t.ID] {
			continue
		}
		if meetsFireWindow(t, now) {
			sc.firingID = t.ID
			sc.fired[t.ID] = true
			sc.tick = 0
			return
		}
	}
}

// meetsFireWindow reports whether the task is due to fire at now, independent of
// whether it has already fired today. It ports tui.checkAlarms's per-task guard:
// not done/dismissed, not snoozed, scheduled today, and within [-2min, 0] of its
// time.
func meetsFireWindow(t Task, now time.Time) bool {
	if t.Done || t.Dismissed {
		return false
	}
	if t.Snoozed > 0 && now.Unix() < t.Snoozed {
		return false
	}
	if !shouldFireTodayAt(t, now) {
		return false
	}
	d := timeUntilAt(t, now)
	// The -2min floor means a task overdue by more than 2 minutes at daemon
	// startup is not re-armed; Phase 6 does restart reconstruction for that.
	return d <= 0 && d >= -2*time.Minute
}

// taskByID finds a task in a snapshot slice by its stable ID.
func taskByID(tasks []Task, id int) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}
