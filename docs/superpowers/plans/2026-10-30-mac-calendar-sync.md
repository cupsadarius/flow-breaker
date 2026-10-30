# Mac Calendar Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Today's macOS Calendar events reach the Pi daemon with no manual download.

**Architecture:** A small Swift app (`FlowBreakerSync.app`, EventKit, no window) reads today's events and POSTs them to a new daemon route. Every flow-breaker invocation on macOS starts it with `open -g -j` (a no-op when it already runs). The app writes `calsync.json`; the TUI and the loud nudge warn when it is stale.

**Tech Stack:** Go 1.25 stdlib, Swift (`/usr/bin/swiftc`), EventKit, AppKit.

**Spec:** `docs/superpowers/specs/2026-10-30-mac-calendar-sync-design.md`

## Global Constraints

- No launchd, no login item.
- No new Go dependency.
- `go test ./... -race` must not need macOS Calendar access.
- Pushed events are Source `"maccal"`; feed events stay Source `"calendar"`. Each source reconciles only its own tasks.
- Status file: `<dataDir>/calsync.json` = `{"last_attempt","last_success": RFC3339, "events": int, "error": string}`.
- App path: `~/Applications/FlowBreakerSync.app`, bundle id `com.darius.flowbreaker.sync`.

## Review Focus

1. Feed refresh with no feeds must not drop `maccal` tasks. → Task 1 test.
2. The helper just started after sleep (fresh attempt, old success): no stale warning. → Task 2 test.
3. Helper keeps failing (fresh attempts, old success, error set): warning shows the error. → Task 2 test.
4. App not installed: no `open` call, no output. → Task 3 test.
5. Daemon on Linux (Pi): no `open` call. → Task 3 test (GOOS guard).

---

### Task 1: Daemon route `POST /calendar/events`

**Files:** Modify `store.go`, `daemon.go`, `daemon_routes.go`; Test `daemon_routes_test.go`.

**Interfaces:**
- Produces: `func (s *Store) materializeCalendarFrom(events []CalendarEvent, now time.Time, source string) (added, updated, dropped int)`; `materializeCalendar(events, now)` calls it with `"calendar"`. Reconcile only drops tasks whose `Source == source`.
- Route: `POST /calendar/events` body `{"events": []CalendarEvent}` → 200 `{"added","updated","dropped"}`; 400 on bad JSON.

- [ ] **Step 1:** Failing tests: `TestCalendarEventsRoute` (POST one event `{uid:"e1", summary:"Standup", start_time:"09:00"}` → one task, `Source=="maccal"`, `CalUID=="e1"`); `TestFeedRefreshKeepsMacEvents` (after the POST, `store.materializeCalendar(nil, now)` → the maccal task remains).
- [ ] **Step 2:** `go test -run 'CalendarEventsRoute|FeedRefreshKeeps' ./...` → FAIL (404).
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; full suite PASS.
- [ ] **Step 5:** Commit with `/ledger`.

### Task 2: Staleness warning

**Files:** Create `calsync.go`; Modify `tui.go` (tick reads status into `m.calWarn`, header shows it), `cli.go` (loud nudge prints it); Test `calsync_test.go`.

**Interfaces:**
- Produces: `type calSyncStatus struct{ LastAttempt, LastSuccess string; Events int; Error string }` (JSON tags as in Global Constraints); `func readCalSync() (calSyncStatus, bool)`; `func calSyncWarning(st calSyncStatus, now time.Time) string`.

Rules for `calSyncWarning`:
- Error contains "access" → `"calendar access denied — System Settings → Privacy → Calendars"`.
- Success within 30 min → `""`.
- Error set → `"calendar sync failing: <error>"`.
- Last attempt within 5 min → `""` (helper just started).
- Else → `"calendar sync stale since HH:MM"` (last success, local time; "never" when empty).

- [ ] **Step 1:** Failing table test `TestCalSyncWarning`: fresh success → ""; old success + attempt 1 min ago, no error → ""; old success + attempt 20 min ago → "calendar sync stale since 09:12"; old success + fresh attempt + error "timeout" → "calendar sync failing: timeout"; error "calendar access denied" → access message.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement; wire TUI header (urgent style, between date and daemon dot) and loud nudge (extra line after the body; quiet path unchanged).
- [ ] **Step 4:** PASS; full suite PASS.
- [ ] **Step 5:** Commit with `/ledger`.

### Task 3: Lazy start and `cal-sync --install`

**Files:** Modify `calsync.go`, `main.go`, `cli.go` (usage); Create `macos/FlowBreakerSync/main.swift`, `macos/FlowBreakerSync/Info.plist`; Test `calsync_test.go`.

**Interfaces:**
- Produces: `var openApp = func(path string) error` (runs `open -g -j path`, does not wait); `func startCalSync(goos, home string)` calls `openApp` only when `goos == "darwin"` and the app dir exists; `func cliCalSync(args []string)` handles `--install`.
- `main()` calls `startCalSync(runtime.GOOS, home)` for every command except `daemon`.
- `--install`: write embedded `main.swift` to a temp dir; `swiftc -O -framework EventKit -framework AppKit -o <app>/Contents/MacOS/FlowBreakerSync main.swift`; write `Info.plist`; `codesign --force -s - <app>`; `open -g -j <app>`; print the setup steps from the spec.

Swift app behavior (`main.swift`):
- `NSApplication` with `.accessory` policy; request full calendar access once.
- Daemon URL: `FLOW_BREAKER_URL` env, else `~/.config/flow-breaker/client`, else `http://localhost:7777`. Data dir: `FLOW_BREAKER_DIR`, else `~/.flow-breaker`.
- Calendars: those whose `source.title` contains `darius@optura.ai`; if none, all event calendars.
- Sync = today's events, skip all-day and `.canceled`; `declined` = the current user's attendee status is `.declined`; `uid` = `calendarItemExternalIdentifier` + start `HH:MM`; `start_time`/`end_time` = local `HH:MM`; POST `{"events": [...]}` to `/calendar/events`; write `calsync.json` after every attempt.
- Sync at start, on `.EKEventStoreChanged`, and every 900 s.

- [ ] **Step 1:** Failing tests: `TestStartCalSyncOnlyWhenInstalled` (temp home without app → `openApp` not called; with `Applications/FlowBreakerSync.app` dir → called once with that path); `TestStartCalSyncSkipsLinux` (app present, goos "linux" → not called).
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement Go side and the Swift app.
- [ ] **Step 4:** PASS; full suite PASS; `swiftc -typecheck macos/FlowBreakerSync/main.swift` clean.
- [ ] **Step 5:** Commit with `/ledger`.
