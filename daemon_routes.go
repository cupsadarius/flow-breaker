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

// handleStatus handles GET /status. The scheduler tracks the firing task by
// stable ID; here it is resolved to its current slice index and the report is
// built under ONE read lock, so a concurrent write (add/edit/delete →
// sortTasks) cannot move the task between resolving the index and reading it.
func handleStatus(store *Store, sc *scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		// Firing task no longer exists (e.g. deleted): clear the stale state.
		// Done after releasing the read lock — clearFiring takes the scheduler
		// lock only, not the store lock.
		if id != 0 && alarm == nil {
			sc.clearFiring(id)
		}
		writeJSON(w, http.StatusOK, report)
	}
}
