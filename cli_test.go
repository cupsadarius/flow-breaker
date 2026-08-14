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
