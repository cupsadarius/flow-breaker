package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// calSyncStatus is calsync.json, written by FlowBreakerSync.app after every
// sync attempt.
type calSyncStatus struct {
	LastAttempt string `json:"last_attempt"`
	LastSuccess string `json:"last_success"`
	Events      int    `json:"events"`
	Error       string `json:"error"`
}

func readCalSync() (calSyncStatus, bool) {
	var st calSyncStatus
	data, err := os.ReadFile(filepath.Join(dataDir(), "calsync.json"))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, true
}

// calSyncWarning returns a one-line warning, or "" when sync is healthy or the
// helper only just started (a fresh attempt after sleep).
func calSyncWarning(st calSyncStatus, now time.Time) string {
	if strings.Contains(st.Error, "access") {
		return "calendar access denied — System Settings → Privacy → Calendars"
	}
	within := func(ts string, d time.Duration) bool {
		t, err := time.Parse(time.RFC3339, ts)
		return err == nil && now.Sub(t) < d
	}
	if within(st.LastSuccess, 30*time.Minute) {
		return ""
	}
	if st.Error != "" {
		return "calendar sync failing: " + st.Error
	}
	if within(st.LastAttempt, 5*time.Minute) {
		return ""
	}
	since := "never"
	if t, err := time.Parse(time.RFC3339, st.LastSuccess); err == nil {
		since = t.Local().Format("15:04")
	}
	return "calendar sync stale since " + since
}

// calSyncNotice is the warning for the current status file; "" when the
// helper is not installed.
func calSyncNotice() string {
	st, ok := readCalSync()
	if !ok {
		return ""
	}
	return calSyncWarning(st, time.Now())
}
