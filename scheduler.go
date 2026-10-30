package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// The escalation ladder (per task, independent). Elapsed since a ladder's first
// fire drives it:
//
//	0:00   → ntfy priority 3, republish every 60s while the ladder lives
//	≥3:00  → step to priority 4
//	≥6:00  → step to priority 5
//	≥10:00 → fire Telegram once
//	≥15:00 → terminal: if the task has NOT auto-snoozed before, snooze it +30m
//	         and remember it auto-snoozed (drop the ladder); if it HAS already
//	         auto-snoozed once, set the task's Missed state and drop the ladder.
const (
	ladderPrio4At    = 3 * time.Minute
	ladderPrio5At    = 6 * time.Minute
	ladderTelegramAt = 10 * time.Minute
	ladderTerminalAt = 15 * time.Minute
	ladderRepublish  = 60 * time.Second
	ladderTgRetry    = 60 * time.Second
	ladderAutoSnooze = 30 // minutes
)

// ladder is the RAM-only escalation state for one task. Nothing here is
// persisted: on daemon restart every escalation-eligible past-due task gets a
// fresh p3 ladder (position reset to p3) — that IS the reconstruction.
type ladder struct {
	firedAt       time.Time
	prio          int
	lastPublish   time.Time // zero = never published
	lastTgAttempt time.Time // zero = never attempted; throttles Telegram retries
	tgFired       bool      // Telegram delivered (latched only on success)
	delivered     bool      // at least one publish OR telegram has actually landed
}

// scheduler owns the per-task escalation ladders in the daemon. It re-evaluates
// once per second and keeps an independent ladder per task ID (never a slice
// index, since concurrent edits call sortTasks() and reorder s.Tasks).
//
// LOCKING DISCIPLINE: store.mu and sc.mu are never held nested. Network I/O and
// store writes happen only in the execute stage of tickOnce, with NO lock held —
// a slow ntfy POST must never block /status's firing() or nest under sc.mu.
type scheduler struct {
	store *Store

	mu          sync.Mutex      // guards ladders, autoSnoozed, dayKey
	ladders     map[int]*ladder // taskID -> active ladder
	autoSnoozed map[int]bool    // taskID -> already auto-snoozed once (2nd run -> missed)
	dayKey      string

	// Injectable outputs (default: wired from config; tests inject spies). Each
	// is a no-op returning nil when its channel is unconfigured.
	publish  func(ctx context.Context, prio int, title, body string, actions []NtfyAction) error
	telegram func(ctx context.Context, text string) error
}

// newScheduler wires publish/telegram from config over a shared HTTP client with
// a small timeout. When NTFY_URL is empty (unconfigured, local dev/tests),
// publish is a no-op returning nil (does not error the tick); likewise telegram
// when the bot token or chat id are empty.
func newScheduler(store *Store) *scheduler {
	client := &http.Client{Timeout: 5 * time.Second}
	sc := &scheduler{
		store:       store,
		ladders:     map[int]*ladder{},
		autoSnoozed: map[int]bool{},
	}
	sc.publish = func(ctx context.Context, prio int, title, body string, actions []NtfyAction) error {
		baseURL, topic := ntfyConfig()
		if baseURL == "" {
			return nil
		}
		return publishNtfy(ctx, client, baseURL, topic, title, body, prio, actions)
	}
	sc.telegram = func(ctx context.Context, text string) error {
		apiBase, token, chatID := telegramConfig()
		if token == "" || chatID == "" {
			return nil
		}
		return sendTelegram(ctx, client, apiBase, token, chatID, text)
	}
	return sc
}

// run ticks once per second forever (the daemon lives for the process
// lifetime). Tests drive tickOnce directly with an injected clock instead.
func (sc *scheduler) run() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		sc.tickOnce(time.Now())
	}
}

// firing returns a representative firing task ID (the ladder with the earliest
// firedAt), or 0 if no ladder is active. Used by GET /status.
func (sc *scheduler) firing() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	best := 0
	var bestAt time.Time
	for id, l := range sc.ladders {
		// Earliest firedAt wins; ties broken by lowest task ID so /status is stable
		// across ticks (map iteration order is random) when ladders share firedAt.
		if best == 0 || l.firedAt.Before(bestAt) || (l.firedAt.Equal(bestAt) && id < best) {
			best = id
			bestAt = l.firedAt
		}
	}
	return best
}

// clearFiring drops the ladder for id. Used by the /status handler when a firing
// task can no longer be resolved (e.g. it was deleted); the next tick would prune
// it anyway, but clearing eagerly avoids re-reporting a phantom alarm.
func (sc *scheduler) clearFiring(id int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	delete(sc.ladders, id)
}

// escalationEligible reports whether a task should have an escalation ladder at
// now: scheduled today, past due (NO -2min floor — this is also how restart
// reconstruction re-arms past-due tasks at p3), and not done/dismissed/missed
// and not currently snoozed.
func escalationEligible(t Task, now time.Time) bool {
	if t.Done || t.Dismissed || t.Missed {
		return false
	}
	if t.Snoozed > 0 && now.Unix() < t.Snoozed {
		return false
	}
	if !shouldFireTodayAt(t, now) {
		return false
	}
	return timeUntilAt(t, now) <= 0
}

// publishReq / telegramReq / terminalReq are planned side-effects built under
// sc.mu and executed after the lock is released.
type publishReq struct {
	id      int
	prio    int
	title   string
	body    string
	actions []NtfyAction
}

type telegramReq struct {
	id   int
	text string
}

type terminalReq struct {
	id     int
	missed bool // true => markMissed; false => auto-snooze +30m
}

// tickOnce re-evaluates every ladder for the given instant in three stages,
// respecting the no-nested-lock rule:
//  1. Snapshot tasks under store.mu.RLock(), release.
//  2. Plan under sc.mu: day rollover, prune, create, advance — building the
//     side-effect plan and updating ladder RAM state optimistically. Release.
//  3. Execute with NO lock held: run publish/telegram, apply terminal mutations.
func (sc *scheduler) tickOnce(now time.Time) {
	// Stage 1: snapshot.
	sc.store.mu.RLock()
	tasks := make([]Task, len(sc.store.Tasks))
	copy(tasks, sc.store.Tasks)
	sc.store.mu.RUnlock()

	// Stage 2: plan under sc.mu.
	sc.mu.Lock()

	// Day rollover: forget yesterday's auto-snooze memory so a task that missed
	// yesterday starts today's ladders fresh, and flag the store to roll over too
	// (resetDaily runs lock-free in stage 3). The first tick (empty dayKey) is not
	// a rollover — the daemon already called resetDaily at startup.
	rolledOver := false
	if day := now.Format("2006-01-02"); day != sc.dayKey {
		if sc.dayKey != "" {
			rolledOver = true
		}
		sc.autoSnoozed = map[int]bool{}
		sc.dayKey = day
	}

	// Prune: drop ladders whose task is gone or no longer eligible. This is ack
	// teardown — done/dismiss/snooze all make a task ineligible, so its ladder
	// drops here with no handler coupling.
	for id := range sc.ladders {
		t, ok := taskByID(tasks, id)
		if !ok || !escalationEligible(t, now) {
			delete(sc.ladders, id)
		}
	}

	// Create: a p3 ladder for each eligible task without one.
	for _, t := range tasks {
		if !escalationEligible(t, now) {
			continue
		}
		if _, ok := sc.ladders[t.ID]; !ok {
			sc.ladders[t.ID] = &ladder{firedAt: now, prio: 3}
		}
	}

	// Advance: step each ladder and build the side-effect plan.
	var publishes []publishReq
	var telegrams []telegramReq
	var terminals []terminalReq

	for id, l := range sc.ladders {
		t, ok := taskByID(tasks, id)
		if !ok {
			continue // pruned above; defensive
		}
		elapsed := now.Sub(l.firedAt)

		// Terminal — but ONLY once an alert has actually been delivered. Never
		// silence an alarm that was never seen: if nothing has landed by 15m
		// (ntfy + telegram both down), keep the ladder alive and keep retrying;
		// the terminal snooze/Missed only fires after a real delivery.
		if elapsed >= ladderTerminalAt {
			if l.delivered {
				// The terminal store mutation and the ladder's RAM teardown
				// (delete + autoSnoozed) are committed in stage 3 AFTER the store
				// write succeeds, so a failed write can't downgrade the two-strike
				// guarantee. missed reflects whether this task already auto-snoozed.
				terminals = append(terminals, terminalReq{id: id, missed: sc.autoSnoozed[id]})
				continue
			}
			log.Printf("flow-breaker: task %d unacked >=15m but NO alert has been delivered (ntfy/telegram down?); keeping ladder alive and retrying", id)
			// Fall through: keep republishing / retrying telegram below.
		}

		// Step priority from elapsed.
		switch {
		case elapsed >= ladderPrio5At:
			l.prio = 5
		case elapsed >= ladderPrio4At:
			l.prio = 4
		default:
			l.prio = 3
		}

		// Republish when never published or the cadence has elapsed. lastPublish is
		// latched optimistically to bound the cadence; delivered is set only on a
		// successful send (stage 3).
		if l.lastPublish.IsZero() || now.Sub(l.lastPublish) >= ladderRepublish {
			publishes = append(publishes, publishReq{
				id:      id,
				prio:    l.prio,
				title:   "Flow Breaker",
				body:    taskHHMM(t, now) + " — " + t.Desc,
				actions: escalationActions(id),
			})
			l.lastPublish = now
		}

		// Telegram fallback at 10:00 — attempted at most once per 60s until it
		// lands. lastTgAttempt is latched optimistically (throttle); tgFired is set
		// only on a successful send (stage 3), so a failed attempt is retried.
		if elapsed >= ladderTelegramAt && !l.tgFired {
			if l.lastTgAttempt.IsZero() || now.Sub(l.lastTgAttempt) >= ladderTgRetry {
				telegrams = append(telegrams, telegramReq{
					id:   id,
					text: fmt.Sprintf("⏰ %s (%s) unacked 10min", t.Desc, taskHHMM(t, now)),
				})
				l.lastTgAttempt = now
			}
		}
	}

	sc.mu.Unlock()

	// Stage 3: execute with NO store/scheduler lock held during network I/O.
	// After a successful send we briefly re-acquire sc.mu to latch delivery flags
	// on the ladder if it still exists. tickOnce is the only ladder mutator and
	// runs sequentially, so this short critical section (no network, no store
	// lock) is safe and never nests locks.

	// Day rollover: keep the store in step with the scheduler's dayKey so
	// Missed/Done/Snoozed clear at midnight and tasks re-arm. Idempotent
	// (resetDaily guards on LastReset==today).
	if rolledOver {
		sc.store.resetDaily()
	}

	for _, p := range publishes {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := sc.publish(ctx, p.prio, p.title, p.body, p.actions)
		cancel()
		if err != nil {
			log.Printf("flow-breaker: escalation publish failed: %v", err)
			continue
		}
		sc.mu.Lock()
		if l, ok := sc.ladders[p.id]; ok {
			l.delivered = true
		}
		sc.mu.Unlock()
	}
	for _, tg := range telegrams {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := sc.telegram(ctx, tg.text)
		cancel()
		if err != nil {
			log.Printf("flow-breaker: escalation telegram failed: %v", err)
			continue
		}
		sc.mu.Lock()
		if l, ok := sc.ladders[tg.id]; ok {
			l.delivered = true
			l.tgFired = true
		}
		sc.mu.Unlock()
	}
	for _, term := range terminals {
		if term.missed {
			if _, err := sc.store.markMissed(term.id); err != nil {
				log.Printf("flow-breaker: escalation mark-missed failed: %v", err)
				continue // leave the ladder for the next tick
			}
			sc.mu.Lock()
			delete(sc.ladders, term.id)
			sc.mu.Unlock()
		} else {
			if _, err := sc.store.snoozeTask(term.id, ladderAutoSnooze, "escalation"); err != nil {
				log.Printf("flow-breaker: escalation auto-snooze failed: %v", err)
				continue // leave the ladder for the next tick
			}
			sc.mu.Lock()
			sc.autoSnoozed[term.id] = true
			delete(sc.ladders, term.id)
			sc.mu.Unlock()
		}
	}
}

// escalationActions builds the ntfy Done/Snooze action buttons targeting the
// daemon's public base URL. When publicBaseURL() is empty the actions are
// omitted (no host to POST back to).
func escalationActions(id int) []NtfyAction {
	base := publicBaseURL()
	if base == "" {
		return nil
	}
	return []NtfyAction{
		{Label: "Done", URL: fmt.Sprintf("%s/task/%d/done", base, id)},
		{Label: "Snooze", URL: fmt.Sprintf("%s/task/%d/snooze", base, id)},
	}
}

// taskHHMM renders the task's fire time as HH:MM, falling back to the raw Time
// string if it cannot be parsed.
func taskHHMM(t Task, now time.Time) string {
	if tm, ok := parseTaskTimeAt(t.Time, now); ok {
		return tm.Format("15:04")
	}
	return t.Time
}

// taskByID finds a task in a snapshot slice by its stable ID.
func taskByID(tasks []Task, id int) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}
