package main

import (
	"log"
	"net/http"
	"os"
	"time"
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
	mux.HandleFunc("POST /capture", handleCapture(store))
	mux.HandleFunc("POST /task/{id}/done", handleDone(store))
	mux.HandleFunc("POST /task/{id}/snooze", handleSnooze(store))
	mux.HandleFunc("POST /task/{id}/dismiss", handleDismiss(store))
	mux.HandleFunc("POST /task/{id}", handleEdit(store))
	mux.HandleFunc("DELETE /task/{id}", handleDelete(store))
	mux.HandleFunc("POST /inbox/{id}/schedule", handleInboxSchedule(store))
	mux.HandleFunc("DELETE /inbox/{id}", handleInboxDelete(store))

	// Read routes.
	mux.HandleFunc("GET /tasks", handleTasks(store))
	mux.HandleFunc("GET /status", handleStatus(store, sc))
	mux.HandleFunc("GET /nudge", handleNudge(store, sc))
	mux.HandleFunc("GET /settings", handleGetSettings(store))
	mux.HandleFunc("GET /inbox", handleInbox(store))
	mux.HandleFunc("GET /habits", handleHabits(store))

	return mux
}

// runDaemon loads the store and serves the daemon HTTP API.
func runDaemon() {
	store := loadStore()
	store.resetDaily()
	sc := newScheduler(store)
	go sc.run()
	// The daemon is the sole owner of the calendar fetch: it periodically pulls
	// the configured iCal feeds and materializes today's events into tasks, so
	// clients see them via /status and /tasks without fetching anything.
	go runCalendarRefresh(store)
	mux := newDaemonMux(store, sc)
	addr := daemonAddr()
	log.Printf("flow-breaker daemon on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// calRefreshStartupDelay is how long after boot the first calendar refresh runs.
// A short delay (rather than a full interval) means calendar tasks appear soon
// after startup without racing the HTTP listener coming up.
var calRefreshStartupDelay = 5 * time.Second

// calendarRefreshInterval resolves the refresh cadence from settings, guarding
// against a non-positive CalCacheMins (which would make time.NewTicker panic).
func calendarRefreshInterval(store *Store) time.Duration {
	store.mu.RLock()
	mins := store.Settings.CalCacheMins
	store.mu.RUnlock()
	if mins <= 0 {
		mins = 15
	}
	return time.Duration(mins) * time.Minute
}

// runCalendarRefresh runs periodic calendar refreshes for the life of the
// process: one shortly after startup, then on a ticker at the cache interval.
// It never returns (the goroutine lives as long as the daemon).
func runCalendarRefresh(store *Store) {
	refreshOnce := func() {
		added, updated, dropped, err := refreshCalendar(store)
		if err != nil {
			log.Printf("flow-breaker: calendar refresh failed: %v", err)
			return
		}
		if added+updated+dropped > 0 {
			log.Printf("flow-breaker: calendar refresh (added %d, updated %d, dropped %d)",
				added, updated, dropped)
		}
	}

	time.Sleep(calRefreshStartupDelay)
	refreshOnce()

	ticker := time.NewTicker(calendarRefreshInterval(store))
	defer ticker.Stop()
	for range ticker.C {
		refreshOnce()
	}
}

// refreshCalendar performs one calendar refresh cycle and returns the
// materialize counts. When calendar integration is disabled it is a no-op; when
// no feeds are configured it is a no-op. Otherwise it loads the feeds, fetches
// (cache-respecting) today's events, and materializes them into the store.
//
// The store settings are read under a brief RLock and released BEFORE the
// network fetch, and materializeCalendar takes the write lock internally — so
// store.mu is never held across the fetch.
func refreshCalendar(store *Store) (added, updated, dropped int, err error) {
	store.mu.RLock()
	enabled := store.Settings.CalEnabled
	cacheMins := store.Settings.CalCacheMins
	store.mu.RUnlock()

	if !enabled {
		return 0, 0, 0, nil
	}
	feeds, err := loadFeeds()
	if err != nil {
		return 0, 0, 0, err
	}
	if len(feeds) == 0 {
		return 0, 0, 0, nil
	}
	events, err := getCachedOrFetchEvents(feeds, cacheMins)
	if err != nil {
		return 0, 0, 0, err
	}
	added, updated, dropped = store.materializeCalendar(events, time.Now())
	return added, updated, dropped, nil
}
