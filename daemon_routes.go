package main

import (
	"encoding/json"
	"net/http"
	"strconv"
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
		t := store.addTaskSource(body.Time, body.Desc, rec, body.Tags, body.Days, defaultSource(body.Source))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": t})
	}
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
		store.deleteTask(id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleTasks handles GET /tasks — the active (non-archived) tasks as an array.
func handleTasks(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tasks := store.ActiveTasks()
		if tasks == nil {
			tasks = []Task{}
		}
		writeJSON(w, http.StatusOK, tasks)
	}
}

// handleStatus handles GET /status. The scheduler that populates the alarm is
// Phase 1B, so a nil alarm is passed here (alarm_firing is always false).
func handleStatus(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildStatus(store, nil))
	}
}
