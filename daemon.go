package main

import (
	"log"
	"net/http"
	"os"
)

// daemonAddr returns the address the daemon listens on, from FLOW_BREAKER_ADDR
// or the default ":7777".
func daemonAddr() string {
	if addr := os.Getenv("FLOW_BREAKER_ADDR"); addr != "" {
		return addr
	}
	return ":7777"
}

// newDaemonMux builds the HTTP routes served by the daemon.
func newDaemonMux(store *Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Mutation routes (each persists the source field).
	mux.HandleFunc("POST /task", handleAddTask(store))
	mux.HandleFunc("POST /task/{id}/done", handleDone(store))
	mux.HandleFunc("POST /task/{id}/snooze", handleSnooze(store))
	mux.HandleFunc("POST /task/{id}", handleEdit(store))
	mux.HandleFunc("DELETE /task/{id}", handleDelete(store))

	// Read routes.
	mux.HandleFunc("GET /tasks", handleTasks(store))
	mux.HandleFunc("GET /status", handleStatus(store))

	return mux
}

// runDaemon loads the store and serves the daemon HTTP API.
func runDaemon() {
	store := loadStore()
	store.resetDaily()
	mux := newDaemonMux(store)
	addr := daemonAddr()
	log.Printf("flow-breaker daemon on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
