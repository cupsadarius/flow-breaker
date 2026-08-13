package main

import (
	"log"
	"net/http"
	"os"
)

// daemonAddr returns the address the daemon listens on, from FLOW_BREAKER_ADDR
// or the default "127.0.0.1:7777". The default binds loopback only: the daemon
// is tailnet-only single-user, reached via `tailscale serve --bg 7777` proxying
// to localhost, so it must never be exposed on the LAN. The env override lets a
// host bind a specific interface if ever needed.
func daemonAddr() string {
	if addr := os.Getenv("FLOW_BREAKER_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:7777"
}

// newDaemonMux builds the HTTP routes served by the daemon. The scheduler
// supplies the alarm-firing state read by GET /status.
func newDaemonMux(store *Store, sc *scheduler) *http.ServeMux {
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
	mux.HandleFunc("GET /status", handleStatus(store, sc))

	return mux
}

// runDaemon loads the store and serves the daemon HTTP API.
func runDaemon() {
	store := loadStore()
	store.resetDaily()
	sc := newScheduler(store)
	go sc.run()
	mux := newDaemonMux(store, sc)
	addr := daemonAddr()
	log.Printf("flow-breaker daemon on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
