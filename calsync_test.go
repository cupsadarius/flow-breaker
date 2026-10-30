package main

import (
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
