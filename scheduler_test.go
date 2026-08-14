package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// schedNow is a fixed reference instant used across scheduler tests so ladders
// are evaluated deterministically (never against the wall clock).
var schedNow = time.Date(2026, 1, 5, 10, 0, 30, 0, time.Local) // a Monday, 10:00:30

// dueTask builds a Daily task whose HH:MM is `offset` away from schedNow.
func dueTask(id int, offset time.Duration) Task {
	return Task{ID: id, Time: schedNow.Add(offset).Format("15:04"), Recurrence: Daily, Desc: "task"}
}

// spyPub records publish calls; spyTg records telegram calls. A scheduler wired
// with these never touches the network.
type pubCall struct {
	prio    int
	title   string
	body    string
	actions []NtfyAction
}

type spy struct {
	mu   sync.Mutex
	pubs []pubCall
	tgs  []string
}

func (s *spy) wire(sc *scheduler) {
	sc.publish = func(_ context.Context, prio int, title, body string, actions []NtfyAction) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pubs = append(s.pubs, pubCall{prio, title, body, actions})
		return nil
	}
	sc.telegram = func(_ context.Context, text string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.tgs = append(s.tgs, text)
		return nil
	}
}

func (s *spy) pubCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pubs)
}

func (s *spy) lastPub() pubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubs[len(s.pubs)-1]
}

func (s *spy) tgCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tgs)
}

func newSpyScheduler(store *Store) (*scheduler, *spy) {
	sc := newScheduler(store)
	sp := &spy{}
	sp.wire(sc)
	return sc, sp
}

// TestLadderProgression drives one ladder through the full escalation ladder.
func TestLadderProgression(t *testing.T) {
	task := dueTask(1, 0) // due now
	store := &Store{Tasks: []Task{task}, NextID: 2, Settings: defaultSettings()}
	sc, sp := newSpyScheduler(store)

	// t=0 → publish p3 with Done+Snooze actions (no public URL → nil actions, so
	// set one to assert action shape).
	t.Setenv("FLOW_BREAKER_PUBLIC_URL", "https://pi.example")
	sc.tickOnce(schedNow)
	if sp.pubCount() != 1 {
		t.Fatalf("t=0: pubCount = %d, want 1", sp.pubCount())
	}
	p := sp.lastPub()
	if p.prio != 3 {
		t.Fatalf("t=0: prio = %d, want 3", p.prio)
	}
	if len(p.actions) != 2 || p.actions[0].Label != "Done" || p.actions[1].Label != "Snooze" {
		t.Fatalf("t=0: actions = %+v, want Done+Snooze", p.actions)
	}
	if p.title != "Flow Breaker" {
		t.Fatalf("t=0: title = %q", p.title)
	}

	// t=+30s → no republish.
	sc.tickOnce(schedNow.Add(30 * time.Second))
	if sp.pubCount() != 1 {
		t.Fatalf("t=+30s: pubCount = %d, want 1 (no republish)", sp.pubCount())
	}

	// t=+60s → republish (still p3).
	sc.tickOnce(schedNow.Add(60 * time.Second))
	if sp.pubCount() != 2 {
		t.Fatalf("t=+60s: pubCount = %d, want 2", sp.pubCount())
	}
	if sp.lastPub().prio != 3 {
		t.Fatalf("t=+60s: prio = %d, want 3", sp.lastPub().prio)
	}

	// t=+3m → prio 4.
	sc.tickOnce(schedNow.Add(3 * time.Minute))
	if got := sp.lastPub().prio; got != 4 {
		t.Fatalf("t=+3m: prio = %d, want 4", got)
	}

	// t=+6m → prio 5.
	sc.tickOnce(schedNow.Add(6 * time.Minute))
	if got := sp.lastPub().prio; got != 5 {
		t.Fatalf("t=+6m: prio = %d, want 5", got)
	}

	// t=+10m → telegram once.
	sc.tickOnce(schedNow.Add(10 * time.Minute))
	if sp.tgCount() != 1 {
		t.Fatalf("t=+10m: tgCount = %d, want 1", sp.tgCount())
	}
	// t=+11m → not twice.
	sc.tickOnce(schedNow.Add(11 * time.Minute))
	if sp.tgCount() != 1 {
		t.Fatalf("t=+11m: tgCount = %d, want still 1", sp.tgCount())
	}

	// t=+15m → terminal: auto-snoozed (not missed), ladder gone.
	sc.tickOnce(schedNow.Add(15 * time.Minute))
	if _, ok := sc.ladders[1]; ok {
		t.Fatalf("t=+15m: ladder still present, want gone")
	}
	store.mu.RLock()
	snoozed := store.Tasks[0].Snoozed
	missed := store.Tasks[0].Missed
	store.mu.RUnlock()
	if missed {
		t.Fatalf("t=+15m: task Missed, want auto-snoozed on first run")
	}
	// snoozeTask uses wall clock; assert it was set ~30m out from now.
	if snoozed < time.Now().Add(28*time.Minute).Unix() {
		t.Fatalf("t=+15m: Snoozed = %d, want ~+30m", snoozed)
	}
	if !sc.autoSnoozed[1] {
		t.Fatalf("t=+15m: autoSnoozed[1] = false, want true")
	}

	// Snooze lapses → simulate by clearing Snoozed → a fresh ladder is created,
	// and because it already auto-snoozed once, its terminal → Missed.
	store.mu.Lock()
	store.Tasks[0].Snoozed = 0
	store.mu.Unlock()

	reNow := schedNow.Add(16 * time.Minute)
	sc.tickOnce(reNow)
	l, ok := sc.ladders[1]
	if !ok {
		t.Fatalf("after lapse: no new ladder, want p3 ladder")
	}
	if l.prio != 3 {
		t.Fatalf("after lapse: prio = %d, want 3 (fresh ladder)", l.prio)
	}

	// Its +15m → task Missed, ladder gone.
	sc.tickOnce(reNow.Add(15 * time.Minute))
	if _, ok := sc.ladders[1]; ok {
		t.Fatalf("second terminal: ladder still present, want gone")
	}
	store.mu.RLock()
	missed = store.Tasks[0].Missed
	store.mu.RUnlock()
	if !missed {
		t.Fatalf("second terminal: task not Missed, want Missed")
	}
}

// TestIndependentLadders: two past-due tasks → two ladders, each publishes.
func TestIndependentLadders(t *testing.T) {
	store := &Store{
		Tasks:    []Task{dueTask(1, 0), dueTask(2, -time.Minute)},
		NextID:   3,
		Settings: defaultSettings(),
	}
	sc, sp := newSpyScheduler(store)

	sc.tickOnce(schedNow)

	if len(sc.ladders) != 2 {
		t.Fatalf("ladders = %d, want 2", len(sc.ladders))
	}
	if sp.pubCount() != 2 {
		t.Fatalf("pubCount = %d, want 2 (one per task)", sp.pubCount())
	}
}

// TestAckTeardown: a firing task marked Done → next tick prunes its ladder.
func TestAckTeardown(t *testing.T) {
	store := &Store{Tasks: []Task{dueTask(1, 0)}, NextID: 2, Settings: defaultSettings()}
	sc, sp := newSpyScheduler(store)

	sc.tickOnce(schedNow)
	if _, ok := sc.ladders[1]; !ok {
		t.Fatalf("precondition: no ladder")
	}
	before := sp.pubCount()

	store.mu.Lock()
	store.Tasks[0].Done = true
	store.mu.Unlock()

	sc.tickOnce(schedNow.Add(60 * time.Second))
	if _, ok := sc.ladders[1]; ok {
		t.Fatalf("ladder still present after Done, want pruned")
	}
	if sp.pubCount() != before {
		t.Fatalf("published after Done: %d → %d, want no new publish", before, sp.pubCount())
	}
}

// TestRestartReconstruction: a fresh scheduler + past-due unacked task → one tick
// creates a p3 ladder. A Missed task → no ladder.
func TestRestartReconstruction(t *testing.T) {
	store := &Store{Tasks: []Task{dueTask(1, -20*time.Minute)}, NextID: 2, Settings: defaultSettings()}
	sc, _ := newSpyScheduler(store)

	sc.tickOnce(schedNow)
	l, ok := sc.ladders[1]
	if !ok {
		t.Fatalf("past-due task: no ladder, want p3 reconstruction")
	}
	if l.prio != 3 {
		t.Fatalf("reconstructed ladder prio = %d, want 3", l.prio)
	}

	// Missed task → excluded, no ladder.
	store2 := &Store{
		Tasks:    []Task{{ID: 5, Time: schedNow.Format("15:04"), Recurrence: Daily, Missed: true}},
		NextID:   6,
		Settings: defaultSettings(),
	}
	sc2, sp2 := newSpyScheduler(store2)
	sc2.tickOnce(schedNow)
	if len(sc2.ladders) != 0 {
		t.Fatalf("missed task: ladders = %d, want 0", len(sc2.ladders))
	}
	if sp2.pubCount() != 0 {
		t.Fatalf("missed task: published %d, want 0", sp2.pubCount())
	}
}

// TestStatusReportsFiring: /status reports the ladder's task as firing.
func TestStatusReportsFiring(t *testing.T) {
	task := dueTask(7, 0)
	store := &Store{Tasks: []Task{task}, NextID: 8, Settings: defaultSettings()}
	sc, _ := newSpyScheduler(store)

	sc.tickOnce(schedNow)
	if sc.firing() != 7 {
		t.Fatalf("precondition: firing() = %d, want 7", sc.firing())
	}

	srv := httptest.NewServer(newDaemonMux(store, sc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var report StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode StatusReport: %v", err)
	}
	if !report.AlarmFiring {
		t.Fatalf("alarm_firing = false, want true")
	}
	if report.AlarmTask == nil || report.AlarmTask.ID != 7 {
		t.Fatalf("alarm_task = %+v, want task id 7", report.AlarmTask)
	}
}

// TestStatusClearsStaleFiring: firing() points at a task that no longer exists →
// status reports no alarm and clears the stale ladder.
func TestStatusClearsStaleFiring(t *testing.T) {
	store := &Store{Tasks: []Task{}, NextID: 1, Settings: defaultSettings()}
	sc, _ := newSpyScheduler(store)
	sc.ladders[42] = &ladder{firedAt: schedNow, prio: 3} // stale

	srv := httptest.NewServer(newDaemonMux(store, sc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var report StatusReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.AlarmFiring {
		t.Fatalf("alarm_firing = true, want false for missing task")
	}
	if sc.firing() != 0 {
		t.Fatalf("stale ladder = %d, want cleared", sc.firing())
	}
}

// TestResetDailyClearsMissed: the daily reset clears the Missed flag.
func TestResetDailyClearsMissed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOW_BREAKER_DIR", dir)
	store := &Store{
		Tasks:     []Task{{ID: 1, Time: "10:00", Recurrence: Daily, Missed: true}},
		NextID:    2,
		LastReset: "2020-01-01", // force a reset
		Settings:  defaultSettings(),
	}
	store.resetDaily()
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.Tasks[0].Missed {
		t.Fatalf("Missed = true after resetDaily, want cleared")
	}
}
