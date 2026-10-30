package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// writeJSON marshals v and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error body {"error": msg} with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// pathID parses the {id} path value as an int, writing a 400 on failure.
func pathID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// defaultSource returns src, or "api" when src is empty.
func defaultSource(src string) string {
	if src == "" {
		return "api"
	}
	return src
}

// handleAddTask handles POST /task — create a task.
func handleAddTask(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Time       string   `json:"time"`
			Desc       string   `json:"desc"`
			Recurrence string   `json:"recurrence"`
			Days       []string `json:"days"`
			Tags       []string `json:"tags"`
			Source     string   `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		rec := Recurrence(body.Recurrence)
		if rec == "" {
			rec = Once
		}
		t := store.addTaskSource(body.Time, "", body.Desc, rec, body.Tags, body.Days, defaultSource(body.Source))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
	}
}

// handleCapture handles POST /capture — free-text capture with the inbox floor.
// It parses the text (heuristics → Haiku → inbox), scheduling a task when a time
// is confidently resolved and otherwise floating the raw capture to the inbox.
// A 200 means captured, nothing else; only a malformed JSON body is a 4xx.
func handleCapture(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text   string `json:"text"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		source := defaultSource(body.Source)
		res, dest, parsedBy := DefaultParse(body.Text, time.Now())

		var task any
		var echo string
		if dest == "scheduled" {
			t := store.addTaskSource(res.Time, res.Date, res.Desc, res.Recurrence, nil, res.Days, source)
			task = t
			echo = captureEcho(res)
		} else {
			desc := res.Desc
			if desc == "" {
				desc = strings.TrimSpace(body.Text)
			}
			task = store.addInbox(desc, source)
			echo = "📥 inbox — " + desc
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"echo":        echo,
			"destination": dest,
			"parsedBy":    parsedBy,
			"task":        task,
		})
	}
}

// captureEcho renders a human-readable one-line summary of a scheduled capture.
// The date prefix is omitted when the task fires today, shown as "tomorrow" for
// the next day, and as the bare YYYY-MM-DD otherwise.
func captureEcho(res ParseResult) string {
	now := time.Now()
	prefix := ""
	switch res.Date {
	case "", now.Format("2006-01-02"):
		// today — no date prefix
	case now.AddDate(0, 0, 1).Format("2006-01-02"):
		prefix = "tomorrow "
	default:
		prefix = res.Date + " "
	}
	return prefix + res.Time + " — " + res.Desc
}

// handleDone handles POST /task/{id}/done.
func handleDone(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Source string `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&body) // body optional
		t, err := store.markDone(id, defaultSource(body.Source))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
	}
}

// handleDismiss handles POST /task/{id}/dismiss — set Dismissed=true. Mirrors
// handleDone; used by the TUI's alarm dismiss and give-up actions.
func handleDismiss(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Source string `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&body) // body optional
		t, err := store.dismissTask(id, defaultSource(body.Source))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
	}
}

// handleGetSettings handles GET /settings — returns the store settings as JSON.
// Writing settings (POST /settings) is deferred to Phase 6.
func handleGetSettings(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store.mu.RLock()
		s := store.Settings
		store.mu.RUnlock()
		writeJSON(w, http.StatusOK, s)
	}
}

// handleSnooze handles POST /task/{id}/snooze. minutes is optional; when
// omitted (or <= 0) the store falls back to Settings.SnoozeMins.
func handleSnooze(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Source  string `json:"source"`
			Minutes int    `json:"minutes"`
		}
		json.NewDecoder(r.Body).Decode(&body) // body optional
		t, err := store.snoozeTask(id, body.Minutes, defaultSource(body.Source))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
	}
}

// handleEdit handles POST /task/{id} — partial edit. Omitted fields are left
// unchanged.
func handleEdit(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Time       *string   `json:"time"`
			Desc       *string   `json:"desc"`
			Recurrence *string   `json:"recurrence"`
			Days       *[]string `json:"days"`
			Tags       *[]string `json:"tags"`
			Source     string    `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var rec *Recurrence
		if body.Recurrence != nil {
			r := Recurrence(*body.Recurrence)
			rec = &r
		}
		t, err := store.editTaskSource(id, body.Time, body.Desc, rec, body.Tags, body.Days, defaultSource(body.Source))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
	}
}

// handleDelete handles DELETE /task/{id}.
func handleDelete(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := store.deleteTaskChecked(id); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleTasks handles GET /tasks. The optional ?scope= query selects which
// tasks to return: "archived" → archived only, "all" → every task, anything
// else (including empty) → active (non-archived) only.
func handleTasks(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tasks []Task
		switch r.URL.Query().Get("scope") {
		case "archived":
			tasks = store.ArchivedTasks()
		case "all":
			tasks = store.AllTasks()
		default:
			tasks = store.ActiveTasks()
		}
		if tasks == nil {
			tasks = []Task{}
		}
		writeJSON(w, http.StatusOK, tasks)
	}
}

// buildReport resolves the scheduler's firing task (tracked by stable ID) to its
// current slice index and builds the StatusReport under ONE read lock, so a
// concurrent write (add/edit/delete → sortTasks) cannot move the task between
// resolving the index and reading it. If the firing task no longer exists (e.g.
// deleted), the stale scheduler state is cleared after the lock is released
// (clearFiring takes the scheduler lock only, not the store lock).
func buildReport(store *Store, sc *scheduler) StatusReport {
	id := sc.firing()

	store.mu.RLock()
	var alarm *alarmState
	if id != 0 {
		idx := -1
		for i := range store.Tasks {
			if store.Tasks[i].ID == id {
				idx = i
				break
			}
		}
		if idx >= 0 {
			alarm = &alarmState{active: true, taskIdx: idx}
		}
	}
	report := buildStatusLocked(store, alarm)
	store.mu.RUnlock()

	if id != 0 && alarm == nil {
		sc.clearFiring(id)
	}
	return report
}

// handleStatus handles GET /status.
func handleStatus(store *Store, sc *scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildReport(store, sc))
	}
}

// interruptWorthy reports whether a nudge is worth breaking into the turn for:
// an alarm is firing, a task is overdue, or the next task is under 10 minutes
// out. The Pi (daemon) owns this gate so every client — Claude Code, opencode,
// Codex — inherits the same quiet policy without reimplementing it.
func interruptWorthy(r StatusReport) bool {
	return r.AlarmFiring || len(r.Overdue) > 0 || (r.Next != nil && r.Next.Seconds < 600)
}

// handleNudge handles GET /nudge — the plain-text nudge line for agents. Loud
// (default) always returns buildStatus(...).Nudge. With ?quiet=1 the daemon
// gates: an EMPTY body (still 200) unless the state is interruptWorthy, in which
// case the Nudge string is returned. The client never has to decide when to
// speak; it just prints whatever the daemon hands back.
func handleNudge(store *Store, sc *scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := buildReport(store, sc)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Query().Get("quiet") == "1" && !interruptWorthy(report) {
			return // gated: empty body, 200
		}
		w.Write([]byte(report.Nudge))
	}
}

// handleHabits handles GET /habits.
func handleHabits(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hs := buildHabits(store.ActiveTasks(), time.Now())
		if hs == nil {
			hs = []Habit{}
		}
		writeJSON(w, http.StatusOK, hs)
	}
}

// handleInbox handles GET /inbox.
func handleInbox(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.InboxItems())
	}
}

// handleInboxSchedule handles POST /inbox/{id}/schedule. The text supplies
// only the time and recurrence; the item keeps its description.
func handleInboxSchedule(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Text   string `json:"text"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		res, dest, _ := DefaultParse(body.Text, time.Now())
		if dest != "scheduled" {
			writeError(w, http.StatusUnprocessableEntity, "no time found")
			return
		}
		t, err := store.scheduleInbox(id, res, defaultSource(body.Source))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": t})
	}
}

// handleInboxDelete handles DELETE /inbox/{id}.
func handleInboxDelete(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := store.deleteInbox(id); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleCalendarEvents handles POST /calendar/events: today's events pushed by
// the Mac calendar helper, materialized as Source "maccal".
func handleCalendarEvents(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []CalendarEvent `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		added, updated, dropped := store.materializeCalendarFrom(body.Events, time.Now(), "maccal")
		writeJSON(w, http.StatusOK, map[string]any{"added": added, "updated": updated, "dropped": dropped})
	}
}
