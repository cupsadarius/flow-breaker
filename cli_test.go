package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// testClient points a Client at the given httptest server.
func testClient(srv *httptest.Server) *Client {
	return &Client{BaseURL: srv.URL, http: srv.Client()}
}

func TestDoneWithClientIntegerID(t *testing.T) {
	store := newTestStore(t)
	a := store.addTaskSource("09:00", "", "standup", Daily, nil, nil, "tui")
	store.addTaskSource("10:00", "", "lunch", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	if err := doneWithClient(testClient(srv), []string{strconv.Itoa(a.ID)}); err != nil {
		t.Fatalf("doneWithClient: %v", err)
	}
	for _, tk := range store.AllTasks() {
		if tk.ID == a.ID && !tk.Done {
			t.Fatalf("task %d not marked done", a.ID)
		}
		if tk.Desc == "lunch" && tk.Done {
			t.Fatal("wrong task marked done (lunch)")
		}
	}
}

func TestDoneWithClientSubstring(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "", "standup", Daily, nil, nil, "tui")
	lunch := store.addTaskSource("10:00", "", "lunch break", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	if err := doneWithClient(testClient(srv), []string{"lunch"}); err != nil {
		t.Fatalf("doneWithClient: %v", err)
	}
	for _, tk := range store.AllTasks() {
		if tk.ID == lunch.ID && !tk.Done {
			t.Fatal("lunch not marked done")
		}
		if tk.Desc == "standup" && tk.Done {
			t.Fatal("standup should not be done")
		}
	}
}

func TestDoneWithClientSubstringMiss(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "", "standup", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	// No match: should not error and should mark nothing done.
	if err := doneWithClient(testClient(srv), []string{"zzz-nope"}); err != nil {
		t.Fatalf("doneWithClient: %v", err)
	}
	for _, tk := range store.AllTasks() {
		if tk.Done {
			t.Fatalf("no task should be done, but %q is", tk.Desc)
		}
	}
}

func TestClearWithClientDeletesAllInclArchived(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "", "active", Daily, nil, nil, "tui")
	arch := store.addTaskSource("10:00", "", "archived", Once, nil, nil, "tui")
	store.mu.Lock()
	for i := range store.Tasks {
		if store.Tasks[i].ID == arch.ID {
			store.Tasks[i].Archived = true
			store.Tasks[i].ArchivedAt = "2026-01-01T00:00:00Z"
		}
	}
	store.mu.Unlock()

	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	n, err := clearWithClient(testClient(srv))
	if err != nil {
		t.Fatalf("clearWithClient: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2 (active + archived)", n)
	}
	if got := store.AllTasks(); len(got) != 0 {
		t.Fatalf("store still has %d tasks, want 0: %+v", len(got), got)
	}
}

// TestCalAddNoArgsPrintsHintAndFails guards Task 2: a bare `cal-add` prints the
// secret-URL hint and returns a usage error (mapped to a non-zero exit).
func TestCalAddNoArgsPrintsHintAndFails(t *testing.T) {
	var buf bytes.Buffer
	err := calAdd(nil, &buf)
	if !errors.Is(err, errCalAddUsage) {
		t.Fatalf("calAdd(nil) err = %v, want errCalAddUsage", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Secret address in iCal format") {
		t.Fatalf("bare cal-add did not print the secret-URL hint:\n%s", out)
	}
	if !strings.Contains(out, "like a password") {
		t.Fatalf("bare cal-add hint missing the password warning:\n%s", out)
	}
}

// TestCalAddHelpPrintsHint guards Task 2: `cal-add --help` prints the hint and
// is a successful (nil-error) invocation.
func TestCalAddHelpPrintsHint(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		var buf bytes.Buffer
		if err := calAdd([]string{flag}, &buf); err != nil {
			t.Fatalf("calAdd(%q) err = %v, want nil", flag, err)
		}
		if !strings.Contains(buf.String(), "Secret address in iCal format") {
			t.Fatalf("cal-add %s did not print the secret-URL hint:\n%s", flag, buf.String())
		}
	}
}

// TestClearWithClientContinuesPast404 uses a stub that lists three tasks but
// 404s the middle one's DELETE — simulating a task removed concurrently between
// the GET and its DELETE. clearWithClient must skip it and finish the rest.
func TestClearWithClientContinuesPast404(t *testing.T) {
	var mu sync.Mutex
	deleted := map[int]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Task{{ID: 1}, {ID: 2}, {ID: 3}})
	})
	mux.HandleFunc("DELETE /task/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		if id == 2 {
			writeError(w, http.StatusNotFound, fmt.Sprintf("task %d not found", id))
			return
		}
		mu.Lock()
		deleted[id] = true
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n, err := clearWithClient(testClient(srv))
	if err != nil {
		t.Fatalf("clearWithClient should tolerate a concurrent 404: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted count = %d, want 2 (1 and 3, skipping 404 on 2)", n)
	}
	if !deleted[1] || !deleted[3] {
		t.Fatalf("expected 1 and 3 deleted, got %v", deleted)
	}
}

// nudgeTextServer serves a fixed body at GET /nudge (and /nudge?quiet=1).
func nudgeTextServer(body string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nudge", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

// TestNudgeLoudUnreachable: loud nudge against a down daemon prints the HONEST
// unreachable line (never a false all-clear) and does not error.
func TestNudgeLoudUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening now
	c := &Client{BaseURL: url, http: &http.Client{}}

	var buf bytes.Buffer
	if err := nudgeWithClient(c, false, &buf); err != nil {
		t.Fatalf("nudgeWithClient loud unreachable err = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "unreachable") || !strings.Contains(out, "tasks unknown") {
		t.Fatalf("loud unreachable output = %q, want the honest unreachable line", out)
	}
	if strings.Contains(out, "All clear") {
		t.Fatalf("loud unreachable must NEVER print a false all-clear, got %q", out)
	}
}

// TestNudgeLoudSuccess: loud nudge prints the daemon's body verbatim.
func TestNudgeLoudSuccess(t *testing.T) {
	srv := nudgeTextServer("⚠️ OVERDUE: dishes (09:00)")
	defer srv.Close()

	var buf bytes.Buffer
	if err := nudgeWithClient(testClient(srv), false, &buf); err != nil {
		t.Fatalf("nudgeWithClient loud success err = %v", err)
	}
	if !strings.Contains(buf.String(), "OVERDUE: dishes") {
		t.Fatalf("loud success output = %q, want the body", buf.String())
	}
}

// TestNudgeQuietSilentOnUnreachable: quiet nudge says NOTHING when the daemon is
// down (fail-open) and does not error.
func TestNudgeQuietSilentOnUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := &Client{BaseURL: url, http: &http.Client{}}

	var buf bytes.Buffer
	if err := nudgeWithClient(c, true, &buf); err != nil {
		t.Fatalf("nudgeWithClient quiet unreachable err = %v, want nil", err)
	}
	if buf.String() != "" {
		t.Fatalf("quiet unreachable must be silent, printed %q", buf.String())
	}
}

// TestNudgeQuietSilentOnEmpty: quiet nudge prints nothing when the daemon gates
// to an empty body.
func TestNudgeQuietSilentOnEmpty(t *testing.T) {
	srv := nudgeTextServer("")
	defer srv.Close()

	var buf bytes.Buffer
	if err := nudgeWithClient(testClient(srv), true, &buf); err != nil {
		t.Fatalf("nudgeWithClient quiet empty err = %v", err)
	}
	if buf.String() != "" {
		t.Fatalf("quiet empty must be silent, printed %q", buf.String())
	}
}

// TestNudgeQuietPrintsNonEmpty: quiet nudge prints a non-empty gated nudge.
func TestNudgeQuietPrintsNonEmpty(t *testing.T) {
	srv := nudgeTextServer("🚨 ALARM: 09:00 — standup (do it NOW)")
	defer srv.Close()

	var buf bytes.Buffer
	if err := nudgeWithClient(testClient(srv), true, &buf); err != nil {
		t.Fatalf("nudgeWithClient quiet nonempty err = %v", err)
	}
	if !strings.Contains(buf.String(), "ALARM") {
		t.Fatalf("quiet nonempty output = %q, want the nudge", buf.String())
	}
}

// TestCliSource covers the source-attribution precedence.
func TestCliSource(t *testing.T) {
	t.Run("explicit env wins", func(t *testing.T) {
		t.Setenv("FLOW_BREAKER_SOURCE", "phone")
		t.Setenv("CLAUDECODE", "1")
		t.Setenv("OPENCODE", "1")
		if got := cliSource(); got != "phone" {
			t.Fatalf("cliSource = %q, want phone", got)
		}
	})
	t.Run("claude code detected", func(t *testing.T) {
		t.Setenv("FLOW_BREAKER_SOURCE", "")
		t.Setenv("CLAUDECODE", "1")
		t.Setenv("OPENCODE", "")
		if got := cliSource(); got != "claude-code" {
			t.Fatalf("cliSource = %q, want claude-code", got)
		}
	})
	t.Run("opencode detected", func(t *testing.T) {
		t.Setenv("FLOW_BREAKER_SOURCE", "")
		t.Setenv("CLAUDECODE", "")
		t.Setenv("OPENCODE", "1")
		if got := cliSource(); got != "opencode" {
			t.Fatalf("cliSource = %q, want opencode", got)
		}
	})
	t.Run("plain cli fallback", func(t *testing.T) {
		t.Setenv("FLOW_BREAKER_SOURCE", "")
		t.Setenv("CLAUDECODE", "")
		t.Setenv("OPENCODE", "")
		if got := cliSource(); got != "cli" {
			t.Fatalf("cliSource = %q, want cli", got)
		}
	})
}

// TestCaptureWithClientSuccess: capture prints the daemon's echo.
func TestCaptureWithClientSuccess(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	var buf bytes.Buffer
	if err := captureWithClient(testClient(srv), "call the plumber", &buf); err != nil {
		t.Fatalf("captureWithClient: %v", err)
	}
	if strings.TrimSpace(buf.String()) == "" {
		t.Fatal("capture printed no echo")
	}
	if !strings.Contains(buf.String(), "call the plumber") {
		t.Fatalf("capture echo = %q, want it to mention the text", buf.String())
	}
	if len(store.Inbox) != 1 {
		t.Fatalf("capture did not reach the store: inbox=%d", len(store.Inbox))
	}
}

// TestCaptureWithClientUnreachable: capture must fail LOUD (return ErrDaemonDown)
// so a dropped capture is never silently swallowed.
func TestCaptureWithClientUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := &Client{BaseURL: url, http: &http.Client{}}

	var buf bytes.Buffer
	err := captureWithClient(c, "call the plumber", &buf)
	if !errors.Is(err, ErrDaemonDown) {
		t.Fatalf("captureWithClient unreachable err = %v, want ErrDaemonDown", err)
	}
	if buf.String() != "" {
		t.Fatalf("capture should print nothing to out on failure, got %q", buf.String())
	}
}

// TestSnoozeWithClientPostsRoute: snooze hits /task/{id}/snooze with the id and
// optional minutes, and reports the snoozed task.
func TestSnoozeWithClientPostsRoute(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /task/{id}/snooze", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": Task{ID: 7, Time: "09:00", Desc: "standup"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	if err := snoozeWithClient(testClient(srv), []string{"7", "15"}, &buf); err != nil {
		t.Fatalf("snoozeWithClient: %v", err)
	}
	if gotPath != "/task/7/snooze" {
		t.Fatalf("posted to %q, want /task/7/snooze", gotPath)
	}
	if gotBody["minutes"] != float64(15) {
		t.Fatalf("minutes = %v, want 15", gotBody["minutes"])
	}
	if !strings.Contains(buf.String(), "standup") {
		t.Fatalf("snooze output = %q, want the task", buf.String())
	}
}

// TestSnoozeWithClientBadID: a non-integer id errors without hitting the daemon.
func TestSnoozeWithClientBadID(t *testing.T) {
	var buf bytes.Buffer
	if err := snoozeWithClient(&Client{}, []string{"not-an-id"}, &buf); err == nil {
		t.Fatal("expected an error for a non-integer id")
	}
}
