package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthRoute(t *testing.T) {
	srv := httptest.NewServer(newDaemonMux(&Store{}, newScheduler(&Store{})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", string(body), "ok")
	}
}

func TestDaemonAddrDefaultLoopback(t *testing.T) {
	t.Setenv("FLOW_BREAKER_ADDR", "")
	if got := daemonAddr(); got != "127.0.0.1:7777" {
		t.Fatalf("daemonAddr() = %q, want 127.0.0.1:7777 (loopback only)", got)
	}
}

func TestDaemonAddrEnvOverride(t *testing.T) {
	t.Setenv("FLOW_BREAKER_ADDR", "0.0.0.0:9999")
	if got := daemonAddr(); got != "0.0.0.0:9999" {
		t.Fatalf("daemonAddr() = %q, want the env override", got)
	}
}

// TestRefreshCalendarDisabledIsNoop guards the CalEnabled gate: when calendar
// integration is off, refreshCalendar must not fetch feeds or touch the store.
func TestRefreshCalendarDisabledIsNoop(t *testing.T) {
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	store := &Store{NextID: 1, Settings: defaultSettings()}
	store.Settings.CalEnabled = false

	added, updated, dropped, err := refreshCalendar(store)
	if err != nil {
		t.Fatalf("refreshCalendar (disabled): %v", err)
	}
	if added != 0 || updated != 0 || dropped != 0 {
		t.Fatalf("counts = (%d,%d,%d), want (0,0,0) when disabled", added, updated, dropped)
	}
	if len(store.Tasks) != 0 {
		t.Fatalf("store gained %d tasks while disabled", len(store.Tasks))
	}
}

// TestRefreshCalendarMaterializesFromFeed drives the whole helper without HTTP:
// a local .ics file feed → getCachedOrFetchEvents → materializeCalendar. It
// asserts the timed event lands as a source:"calendar" task.
func TestRefreshCalendarMaterializesFromFeed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOW_BREAKER_DIR", dir)

	today := time.Now().Format("20060102")
	ical := "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:refresh-test-uid\r\n" +
		"SUMMARY:Refresh Test\r\n" +
		"DTSTART:" + today + "T120000\r\n" +
		"DTEND:" + today + "T130000\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	icsPath := filepath.Join(dir, "cal.ics")
	if err := os.WriteFile(icsPath, []byte(ical), 0o600); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if err := saveFeeds([]CalendarFeed{{URL: icsPath, Label: "test"}}); err != nil {
		t.Fatalf("saveFeeds: %v", err)
	}

	store := &Store{NextID: 1, Settings: defaultSettings()}
	store.Settings.CalEnabled = true

	added, _, _, err := refreshCalendar(store)
	if err != nil {
		t.Fatalf("refreshCalendar: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if len(store.Tasks) != 1 || store.Tasks[0].Source != "calendar" || store.Tasks[0].CalUID != "refresh-test-uid" {
		t.Fatalf("materialized task wrong: %+v", store.Tasks)
	}
}
