package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	seed := store.addTaskSource("09:00", "", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/task/abc/done", map[string]any{"source": "cli"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDismissRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "", "task", Daily, nil, nil, "cli")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d/dismiss", srv.URL, seed.ID), map[string]any{"source": "tui"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["ok"] != true {
		t.Fatalf("ok = %v, want true", m["ok"])
	}
	task := m["task"].(map[string]any)
	if task["dismissed"] != true {
		t.Fatalf("dismissed = %v, want true", task["dismissed"])
	}
	if task["source"] != "tui" {
		t.Fatalf("source = %v, want tui", task["source"])
	}
	got := store.ActiveTasks()
	if !got[0].Dismissed || got[0].Source != "tui" {
		t.Fatalf("store not mutated: %+v", got[0])
	}
}

func TestDismissRouteNotFound(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/task/999/dismiss", map[string]any{"source": "tui"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["error"] == nil {
		t.Fatalf("expected error field, got %v", m)
	}
}

func TestGetSettingsRoute(t *testing.T) {
	store := newTestStore(t)
	store.Settings.SnoozeMins = 7
	store.Settings.CalEnabled = true
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/settings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	defer resp.Body.Close()
	var got Settings
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got.SnoozeMins != 7 {
		t.Fatalf("snooze_mins = %d, want 7", got.SnoozeMins)
	}
	if !got.CalEnabled {
		t.Fatalf("cal_enabled = false, want true")
	}
}

func TestSnoozeRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	seed := store.addTaskSource("09:00", "", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d/snooze", srv.URL, seed.ID), map[string]any{"source": "api", "minutes": 30})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestEditRoute(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "", "old", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	seed := store.addTaskSource("09:00", "", "task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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

func TestDeleteRouteNotFound(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "DELETE", srv.URL+"/task/999", map[string]any{"source": "cli"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["error"] == nil {
		t.Fatalf("expected error field, got %v", m)
	}
}

func TestEditRouteEmptyRecurrenceUnchanged(t *testing.T) {
	store := newTestStore(t)
	seed := store.addTaskSource("09:00", "", "old", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	// recurrence:"" is present but empty → should be treated as "leave unchanged".
	resp := doJSON(t, "POST", fmt.Sprintf("%s/task/%d", srv.URL, seed.ID), map[string]any{
		"desc": "new", "recurrence": "", "source": "codex",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	task := m["task"].(map[string]any)
	if task["recurrence"] != string(Daily) {
		t.Fatalf("recurrence = %v, want daily (unchanged)", task["recurrence"])
	}
	if store.ActiveTasks()[0].Recurrence != Daily {
		t.Fatalf("store recurrence changed: %v", store.ActiveTasks()[0].Recurrence)
	}
}

func TestTasksRoute(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "", "a", Daily, nil, nil, "tui")
	store.addTaskSource("10:00", "", "b", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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

func TestTasksRouteScope(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "", "active", Daily, nil, nil, "tui")
	arch := store.addTaskSource("10:00", "", "archived", Once, nil, nil, "tui")
	// Mark the second task archived directly.
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

	get := func(q string) []map[string]any {
		resp := doJSON(t, "GET", srv.URL+"/tasks"+q, nil)
		defer resp.Body.Close()
		var tasks []map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
			t.Fatalf("decode %q: %v", q, err)
		}
		return tasks
	}

	if got := get(""); len(got) != 1 || got[0]["desc"] != "active" {
		t.Fatalf("default scope = %+v, want [active]", got)
	}
	if got := get("?scope=archived"); len(got) != 1 || got[0]["desc"] != "archived" {
		t.Fatalf("scope=archived = %+v, want [archived]", got)
	}
	if got := get("?scope=all"); len(got) != 2 {
		t.Fatalf("scope=all len = %d, want 2", len(got))
	}
}

func TestStatusRoute(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("09:00", "", "a", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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

// TestCaptureTimed guards Task 3/4: a text with an unambiguous relative time is
// scheduled by heuristics and lands in store.Tasks.
func TestCaptureTimed(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/capture", map[string]any{
		"text":   "wash dishes in 16 min",
		"source": "phone",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["destination"] != "scheduled" {
		t.Fatalf("destination = %v, want scheduled", m["destination"])
	}
	if m["parsedBy"] != "heuristics" {
		t.Fatalf("parsedBy = %v, want heuristics", m["parsedBy"])
	}
	task, ok := m["task"].(map[string]any)
	if !ok {
		t.Fatalf("task missing/wrong type: %v", m["task"])
	}
	if task["source"] != "phone" {
		t.Fatalf("task.source = %v, want phone", task["source"])
	}
	if got := store.ActiveTasks(); len(got) != 1 {
		t.Fatalf("store.Tasks len = %d, want 1", len(got))
	}
	if len(store.Inbox) != 0 {
		t.Fatalf("inbox len = %d, want 0", len(store.Inbox))
	}
}

// TestCaptureTimeless guards Task 3/4: a text with no time signal floors to the
// inbox with parsedBy "none" and the default "api" source.
func TestCaptureTimeless(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/capture", map[string]any{
		"text": "call the plumber",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["destination"] != "inbox" {
		t.Fatalf("destination = %v, want inbox", m["destination"])
	}
	if m["parsedBy"] != "none" {
		t.Fatalf("parsedBy = %v, want none", m["parsedBy"])
	}
	task, ok := m["task"].(map[string]any)
	if !ok {
		t.Fatalf("task missing/wrong type: %v", m["task"])
	}
	if task["source"] != "api" {
		t.Fatalf("task.source = %v, want api (default)", task["source"])
	}
	if len(store.Inbox) != 1 || store.Inbox[0].Desc != "call the plumber" {
		t.Fatalf("inbox not populated: %+v", store.Inbox)
	}
	if got := store.ActiveTasks(); len(got) != 0 {
		t.Fatalf("store.Tasks len = %d, want 0", len(got))
	}
}

// TestCaptureHaikuFloor guards Task 4: a signal-but-unparseable input with no
// ANTHROPIC_API_KEY errors immediately in parseHaiku and floors to the inbox —
// still captured (200), never dropped, no network call.
func TestCaptureHaikuFloor(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", srv.URL+"/capture", map[string]any{
		"text": "do the thing at some point",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (captured)", resp.StatusCode)
	}
	m := decodeMap(t, resp)
	if m["destination"] != "inbox" {
		t.Fatalf("destination = %v, want inbox (floored)", m["destination"])
	}
	if len(store.Inbox) != 1 {
		t.Fatalf("inbox len = %d, want 1", len(store.Inbox))
	}
}

// TestCaptureMalformedBody guards Task 4: a malformed JSON body is the only 4xx
// — the parser/Haiku never errors the request.
func TestCaptureMalformedBody(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/capture", "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestConcurrentAddAndStatus(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
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

// getText performs a plain GET and returns the trimmed body string.
func getText(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.DefaultClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", url, resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

// hhmm formats now+offset as HH:MM for building tasks relative to the clock.
func hhmm(offset time.Duration) string {
	return time.Now().Add(offset).Format("15:04")
}

// TestNudgeRouteLoud: GET /nudge (no quiet) always returns the nudge text, even
// when the daemon's honest answer is the all-clear.
func TestNudgeRouteLoud(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	body := getText(t, srv.URL+"/nudge")
	if strings.TrimSpace(body) == "" {
		t.Fatal("loud /nudge returned empty body, want the all-clear nudge")
	}
	if !strings.Contains(body, "All clear") {
		t.Fatalf("loud /nudge = %q, want the daemon's all-clear line", body)
	}
}

// TestNudgeQuietAllClearEmpty: with no interrupt-worthy state, ?quiet=1 returns
// an empty body (the Pi gates).
func TestNudgeQuietAllClearEmpty(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	if body := getText(t, srv.URL+"/nudge?quiet=1"); body != "" {
		t.Fatalf("quiet all-clear /nudge = %q, want empty", body)
	}
}

// TestNudgeQuietOverdue: an overdue task makes ?quiet=1 non-empty.
func TestNudgeQuietOverdue(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource(hhmm(-2*time.Minute), "", "overdue task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	body := getText(t, srv.URL+"/nudge?quiet=1")
	if strings.TrimSpace(body) == "" {
		t.Fatal("quiet /nudge with an overdue task returned empty, want the nudge")
	}
	if !strings.Contains(body, "OVERDUE") {
		t.Fatalf("quiet overdue /nudge = %q, want an OVERDUE nudge", body)
	}
}

// TestNudgeQuietNextSoon: a task due in ~5 minutes (under the 10-min gate, but
// not overdue) makes ?quiet=1 non-empty.
func TestNudgeQuietNextSoon(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource(hhmm(5*time.Minute), "", "soon task", Daily, nil, nil, "tui")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	if body := getText(t, srv.URL+"/nudge?quiet=1"); strings.TrimSpace(body) == "" {
		t.Fatal("quiet /nudge with a task due in 5min returned empty, want the nudge")
	}
}

// TestInterruptWorthyAlarm guards the alarm arm of the gate directly (a firing
// alarm is hard to synthesize over HTTP without the scheduler).
func TestInterruptWorthyAlarm(t *testing.T) {
	if !interruptWorthy(StatusReport{AlarmFiring: true}) {
		t.Fatal("AlarmFiring report should be interrupt-worthy")
	}
	if interruptWorthy(StatusReport{}) {
		t.Fatal("empty report should not be interrupt-worthy")
	}
	if !interruptWorthy(StatusReport{Overdue: []TaskStatus{{}}}) {
		t.Fatal("overdue report should be interrupt-worthy")
	}
	if !interruptWorthy(StatusReport{Next: &TaskStatus{Seconds: 300}}) {
		t.Fatal("next-in-5min report should be interrupt-worthy")
	}
	if interruptWorthy(StatusReport{Next: &TaskStatus{Seconds: 3600}}) {
		t.Fatal("next-in-1h report should NOT be interrupt-worthy")
	}
}

// TestInterruptWorthyBoundary pins the gate at the 600s edge: the spec is
// strictly < 600, so 600 is NOT interrupt-worthy but 599 is.
func TestInterruptWorthyBoundary(t *testing.T) {
	if interruptWorthy(StatusReport{Next: &TaskStatus{Seconds: 600}}) {
		t.Fatal("Next.Seconds == 600 should NOT be interrupt-worthy (spec is < 600)")
	}
	if !interruptWorthy(StatusReport{Next: &TaskStatus{Seconds: 599}}) {
		t.Fatal("Next.Seconds == 599 should be interrupt-worthy")
	}
}

func TestHabitsRoute(t *testing.T) {
	store := newTestStore(t)
	store.addTaskSource("18:00", "", "read", Daily, nil, nil, "cli")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/habits", nil)
	defer resp.Body.Close()
	var hs []Habit
	if err := json.NewDecoder(resp.Body).Decode(&hs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(hs) != 1 || hs[0].Name != "read" {
		t.Fatalf("habits = %+v, want one named read", hs)
	}
}

func TestInboxScheduleRoute(t *testing.T) {
	store := newTestStore(t)
	item := store.addInbox("call plumber", "cli")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/inbox/%d/schedule", srv.URL, item.ID), map[string]any{"text": "at 15:00"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(store.InboxItems()) != 0 {
		t.Fatalf("inbox not emptied: %+v", store.InboxItems())
	}
	tasks := store.ActiveTasks()
	if len(tasks) != 1 || tasks[0].Desc != "call plumber" || tasks[0].Time != "15:00" || tasks[0].Recurrence != Once {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestInboxScheduleNoTime(t *testing.T) {
	store := newTestStore(t)
	item := store.addInbox("renew passport", "cli")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "POST", fmt.Sprintf("%s/inbox/%d/schedule", srv.URL, item.ID), map[string]any{"text": "someday"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if len(store.InboxItems()) != 1 {
		t.Fatalf("item left the inbox: %+v", store.InboxItems())
	}
}

func TestInboxDeleteRoute(t *testing.T) {
	store := newTestStore(t)
	item := store.addInbox("x", "cli")
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	url := fmt.Sprintf("%s/inbox/%d", srv.URL, item.ID)
	if resp := doJSON(t, "DELETE", url, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("first delete = %d, want 200", resp.StatusCode)
	}
	if len(store.InboxItems()) != 0 {
		t.Fatalf("inbox not empty")
	}
	if resp := doJSON(t, "DELETE", url, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", resp.StatusCode)
	}
}

func TestInboxEmptyIsArray(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newDaemonMux(store, newScheduler(store)))
	defer srv.Close()

	resp := doJSON(t, "GET", srv.URL+"/inbox", nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("body = %q, want []", b)
	}
}
