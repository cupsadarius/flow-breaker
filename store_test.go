package main

import (
	"os"
	"testing"
)

// TestSaveAtomic guards the durable-write path: save() must produce a tasks.json
// that round-trips via loadStore, and must leave no leftover *.tmp file behind
// (a mid-write crash must never corrupt the sole durable copy).
func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOW_BREAKER_DIR", dir)

	s := &Store{NextID: 1, Settings: defaultSettings()}
	s.addTaskSource("09:00", "standup", Daily, nil, nil, "cli")

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

func TestFindTaskByID(t *testing.T) {
	s := &Store{Tasks: []Task{{ID: 1}, {ID: 7}}, NextID: 8}
	if _, err := s.findTaskByID(7); err != nil {
		t.Fatalf("want task 7, got err %v", err)
	}
	if _, err := s.findTaskByID(99); err == nil {
		t.Fatal("want error for missing id 99")
	}
}
