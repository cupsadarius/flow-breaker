package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCalSyncWarning(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	cases := []struct {
		name string
		st   calSyncStatus
		want string
	}{
		{"fresh success", calSyncStatus{LastAttempt: ago(time.Minute), LastSuccess: ago(time.Minute)}, ""},
		{"just started after sleep", calSyncStatus{LastAttempt: ago(time.Minute), LastSuccess: ago(5 * time.Hour)}, ""},
		{"stale", calSyncStatus{LastAttempt: ago(20 * time.Minute), LastSuccess: time.Date(2026, 10, 30, 9, 12, 0, 0, time.Local).Format(time.RFC3339)}, "calendar sync stale since 09:12"},
		{"failing", calSyncStatus{LastAttempt: ago(time.Minute), LastSuccess: ago(time.Hour), Error: "timeout"}, "calendar sync failing: timeout"},
		{"access denied", calSyncStatus{LastAttempt: ago(time.Minute), Error: "calendar access denied"}, "calendar access denied — System Settings → Privacy → Calendars"},
		{"never synced", calSyncStatus{LastAttempt: ago(time.Hour)}, "calendar sync stale since never"},
	}
	for _, c := range cases {
		if got := calSyncWarning(c.st, now); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStartCalSyncOnlyWhenInstalled(t *testing.T) {
	home := t.TempDir()
	var opened []string
	prev := openApp
	openApp = func(p string) error { opened = append(opened, p); return nil }
	defer func() { openApp = prev }()

	startCalSync("darwin", home)
	if len(opened) != 0 {
		t.Fatalf("opened %v without an installed app", opened)
	}
	app := filepath.Join(home, "Applications", "FlowBreakerSync.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	startCalSync("darwin", home)
	if len(opened) != 1 || opened[0] != app {
		t.Fatalf("opened = %v, want [%s]", opened, app)
	}
}

func TestStartCalSyncSkipsLinux(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "Applications", "FlowBreakerSync.app"), 0o755)
	called := false
	prev := openApp
	openApp = func(string) error { called = true; return nil }
	defer func() { openApp = prev }()

	startCalSync("linux", home)
	if called {
		t.Fatal("opened the app on linux")
	}
}
