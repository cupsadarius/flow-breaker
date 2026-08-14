package main

import "testing"

// TestApplySyncErrorGoesOffline verifies a failed daemon refresh flips the model
// to offline while retaining the last-known cache and report (fail loud, never a
// false empty/all-clear).
func TestApplySyncErrorGoesOffline(t *testing.T) {
	prevReport := &StatusReport{Nudge: "old"}
	m := &model{
		store:  &Store{Tasks: []Task{{ID: 1, Desc: "keep"}}},
		report: prevReport,
	}

	m.applySync(syncMsg{err: ErrDaemonDown})

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

	m.applySync(syncMsg{tasks: newTasks, report: rep})

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
