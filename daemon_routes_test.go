package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// newTestStore returns a fresh store whose saves land in a temp dir so tests
// never clobber real ~/.flow-breaker data.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("FLOW_BREAKER_DIR", t.TempDir())
	s := &Store{NextID: 1, Settings: defaultSettings()}
	return s
}

func doJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func decodeMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestAddTaskRoute(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/task", map[string]any{
		"time":       "09:00",
		"desc":       "standup",
		"recurrence": "daily",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["ok"] != true {
		t.Fatalf("ok = %v, want true", m["ok"])
	}
	task, ok := m["task"].(map[string]any)
	if !ok {
		t.Fatalf("task missing/wrong type: %v", m["task"])
	}
	if task["source"] != "api" {
		t.Fatalf("source = %v, want api (default)", task["source"])
	}
	if task["desc"] != "standup" {
		t.Fatalf("desc = %v, want standup", task["desc"])
	}
	if got := store.ActiveTasks(); len(got) != 1 || got[0].Desc != "standup" || got[0].Source != "api" {
		t.Fatalf("store not mutated correctly: %+v", got)
	}
}

func TestAddTaskRouteExplicitSource(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/task", map[string]any{
		"time": "10:00", "desc": "ping", "recurrence": "once", "source": "phone",
	})
	m := decodeMap(t, resp)
	task := m["task"].(map[string]any)
	if task["source"] != "phone" {
		t.Fatalf("source = %v, want phone", task["source"])
	}
}

func TestDoneRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d/done", srv.URL, seed.ID), map[string]any{"source": "cli"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["ok"] != true {
		t.Fatalf("ok = %v", m["ok"])
	}
	task := m["task"].(map[string]any)
	if task["done"] != true {
		t.Fatalf("done = %v, want true", task["done"])
	}
	if task["source"] != "cli" {
		t.Fatalf("source = %v, want cli", task["source"])
	}
	got := store.ActiveTasks()
	if !got[0].Done || got[0].Source != "cli" {
		t.Fatalf("store not mutated: %+v", got[0])
	}
}

func TestDoneRouteNotFound(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/task/999/done", map[string]any{"source": "cli"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["error"] == nil {
		t.Fatalf("expected error field, got %v", m)
	}
}

func TestDoneRouteBadID(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/task/abc/done", map[string]any{"source": "cli"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSnoozeRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	// no minutes → default SnoozeMins
	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d/snooze", srv.URL, seed.ID), map[string]any{"source": "ntfy"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	task := m["task"].(map[string]any)
	if task["source"] != "ntfy" {
		t.Fatalf("source = %v, want ntfy", task["source"])
	}
	if task["snoozed"].(float64) <= 0 {
		t.Fatalf("snoozed = %v, want > 0", task["snoozed"])
	}
	if store.ActiveTasks()[0].Snoozed <= 0 {
		t.Fatalf("store snoozed not set")
	}
}

func TestSnoozeRouteExplicitMinutes(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d/snooze", srv.URL, seed.ID), map[string]any{"source": "api", "minutes": 30})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestEditRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "old", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d", srv.URL, seed.ID), map[string]any{
		"desc": "new", "source": "codex",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	task := m["task"].(map[string]any)
	if task["desc"] != "new" {
		t.Fatalf("desc = %v, want new", task["desc"])
	}
	// time omitted → unchanged
	if task["time"] != "09:00" {
		t.Fatalf("time = %v, want 09:00 (unchanged)", task["time"])
	}
	if task["source"] != "codex" {
		t.Fatalf("source = %v, want codex", task["source"])
	}
	got := store.ActiveTasks()[0]
	if got.Desc != "new" || got.Time != "09:00" || got.Source != "codex" {
		t.Fatalf("store not mutated correctly: %+v", got)
	}
}

func TestDeleteRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "DELETE", fmt.Sprintf("%s/task/%d", srv.URL, seed.ID), map[string]any{"source": "cli"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["ok"] != true {
		t.Fatalf("ok = %v, want true", m["ok"])
	}
	if len(store.ActiveTasks()) != 0 {
		t.Fatalf("task not deleted: %v", store.ActiveTasks())
	}
}

func TestTasksRoute(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "a", Daily, nil, nil, "tui")
	store.addTaskSource("10:00", "b", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/tasks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	defer resp.Body.Close()
	var tasks []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
}

func TestTasksRouteEmpty(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/tasks", nil)
	defer resp.Body.Close()
	var tasks []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode (empty should be [] not null): %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("got %d, want 0", len(tasks))
	}
}

func TestStatusRoute(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "a", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	defer resp.Body.Close()
	var report StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode StatusReport: %v", err)
	}
	if report.Timestamp == "" {
		t.Fatalf("expected timestamp")
	}
	if report.AlarmFiring {
		t.Fatalf("alarm_firing should be false with nil alarm this phase")
	}
	// overdue/upcoming keys must be present (non-nil marshaled arrays)
	m := map[string]any{}
	// re-marshal to inspect keys
	b, _ := json.Marshal(report)
	json.Unmarshal(b, &m)
	if _, ok := m["overdue"]; !ok {
		t.Fatalf("missing overdue key")
	}
	if _, ok := m["upcoming"]; !ok {
		t.Fatalf("missing upcoming key")
	}
	if _, ok := m["nudge"]; !ok {
		t.Fatalf("missing nudge key")
	}
}

func TestConcurrentAddAndStatus(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store))
	defer srv.Close()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			resp := doJSON(t, "POST", srv.URL+"/task", map[string]any{
				"time": "09:00", "desc": fmt.Sprintf("t%d", i), "recurrence": "daily", "source": "api",
			})
			resp.Body.Close()
		}(i)
		go func() {
			defer wg.Done()
			resp := doJSON(t, "GET", srv.URL+"/status", nil)
			resp.Body.Close()
		}()
	}
	wg.Wait()

	if got := len(store.ActiveTasks()); got != n {
		t.Fatalf("got %d tasks, want %d", got, n)
	}
}
