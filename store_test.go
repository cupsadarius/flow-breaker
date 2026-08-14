package main

import "testing"

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
