# TUI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat-table TUI with the two-pane Today | Habits + Inbox layout (variant A).

**Architecture:** The daemon gets habit computation (`GET /habits`) and an inbox API. The TUI stays a pure client: it syncs `/tasks`, `/status`, `/inbox`, `/habits` and renders three focusable panes plus a quick-add bar. Removed features (`p`, `r`, `h`/`f`) lose their code.

**Tech Stack:** Go 1.25, Bubble Tea, Lip Gloss, stdlib `net/http`. No new dependency.

**Spec:** `docs/superpowers/specs/2026-10-30-tui-redesign-design.md`

## Global Constraints

- No new dependency.
- The TUI never writes the store; every mutation goes through the daemon.
- Offline: keep the last cache, show the offline banner, block mutations with "daemon down" (current behavior).
- 16-color ANSI palette only.
- A dismissed slot counts as missed in grid and streak.
- Removed: `p` (calendar suggest/timeline/import), `r` (refresh), `h`/`f` (habit view toggles), and the `a`–`i` calendar quick-fill in the add form.
- Tests: `go test ./... -race` passes after each task.

## Review Focus

1. A habit created mid-week: days before `CreatedAt` show "none", never "missed". → Task 1 test.
2. Two slots of one habit, one done today, one still pending: today is "none", streak counts from yesterday. → Task 1 test.
3. Inbox schedule text with no time ("someday"): 422, item stays in the inbox. → Task 2 test.
4. Daemon offline: mutation keys show "daemon down", panes keep last data. → Task 4 test.
5. Empty store: every pane shows its empty hint, no panic on cursor keys. → Task 4 test.

---

### Task 1: Habit computation

**Files:**
- Create: `habits.go`
- Test: `habits_test.go`

**Interfaces:**
- Produces:
  ```go
  type HabitCell string
  const (CellDone HabitCell = "done"; CellMissed HabitCell = "missed"; CellNone HabitCell = "none")
  type Habit struct {
      Name          string      `json:"name"`
      TaskIDs       []int       `json:"task_ids"`
      Slots         []string    `json:"slots"`  // HH:MM, ascending
      Streak        int         `json:"streak"`
      Cells         []HabitCell `json:"cells"`  // 28; Cells[0] = Monday 3 weeks before this week's Monday
      WeekDone      int         `json:"week_done"`
      WeekScheduled int         `json:"week_scheduled"`
  }
  func buildHabits(tasks []Task, now time.Time) []Habit // sorted by first slot time
  ```
- Consumes: `shouldFireOnDay`, `createdOnDate`, `startOfDay`, `weekStart` (in `tui.go`; move it to `habits.go`).

Rules:
- A habit = non-archived tasks with `Recurrence != Once`, grouped by exact `Desc`.
- A slot is scheduled on day D when `shouldFireOnDay(t, D.Weekday())` and D ≥ `createdOnDate(t)`.
- Past day D: slot done iff `t.History[D] == true`.
- Today: slot done iff `t.Done`; slot missed iff `t.Dismissed || t.Missed`; else pending.
- Day cell: no scheduled slot or future → none; all scheduled slots done → done; any missed (past: any not done) → missed; today with a pending slot and no missed slot → none.
- Streak: walk back from today. done → +1; missed → stop; none → skip. Stop at the earliest `createdOnDate` of the habit's tasks. Max 366 days.
- Week totals: slots scheduled Mon..today; done = slots done on those days.

- [ ] **Step 1: Write failing tests** in `habits_test.go`. Use `now = 2026-10-30 14:00` local (a Friday).
  - `TestBuildHabitsGroupsByDesc`: two daily tasks "push-ups" at 10:00 and 16:30 plus one Once task → one habit, `Slots == ["10:00","16:30"]`, `len(Cells) == 28`.
  - `TestStreakCountsDoneDaysAndSkipsPendingToday`: daily task created 2026-10-20, History true for 10-26..10-29, today not done → `Streak == 4`, `Cells[24]` (Thu 10-29) `== CellDone`, `Cells[25]` (today) `== CellNone`. (`Cells[0]` = Mon 10-05.)
  - `TestDismissedTodayBreaksStreak`: same task, `Dismissed: true` → `Streak == 0`, today cell `CellMissed`.
  - `TestPartialSlotsTodayIsNone`: two slots created 2026-10-29, first `Done`, second pending, History for both on 10-29 → today `CellNone`, `Streak == 1`.
  - `TestDaysBeforeCreationAreNone`: daily task created 2026-10-28, no History → cells for 10-27 and earlier `CellNone`; 10-28 and 10-29 `CellMissed`; `Streak == 0`.
  - `TestWeekdaysHabitSkipsWeekend`: weekdays task created 2026-10-01, History true for every weekday 10-01..10-29 → `Streak == 21`, Saturday 10-25 cell `CellNone`; `WeekScheduled == 5`, `WeekDone == 4`.
- [ ] **Step 2:** `go test -run 'Habit|Streak|Dismissed|Partial|Creation|Weekdays' ./...` → FAIL, `buildHabits` undefined.
- [ ] **Step 3:** Implement `buildHabits` and the types in `habits.go`. Move `weekStart` from `tui.go`.
- [ ] **Step 4:** Same command → PASS. Then `go test ./... -race` → PASS.
- [ ] **Step 5:** Commit with `/ledger` (stage `habits.go habits_test.go tui.go`).

### Task 2: Daemon routes — habits and inbox

**Files:**
- Modify: `daemon.go` (`newDaemonMux`), `daemon_routes.go`, `store.go`
- Test: `daemon_routes_test.go`

**Interfaces:**
- Consumes: `buildHabits(tasks, now)` (Task 1), `DefaultParse(text, now) (ParseResult, string, string)`.
- Produces:
  - `GET /habits` → `[]Habit` (never `null`).
  - `GET /inbox` → `[]Task` (never `null`).
  - `POST /inbox/{id}/schedule` body `{"text": string, "source": string}` → 200 `{"task": Task}`; 404 unknown id; 422 `{"error": "no time found"}` when parse destination is `"inbox"`.
  - `DELETE /inbox/{id}` → 200 `{"ok": true}`; 404 unknown id.
  - Store: `func (s *Store) InboxItems() []Task`, `func (s *Store) scheduleInbox(id int, res ParseResult, source string) (Task, error)` (removes the inbox item, adds a task with `res.Time/Date/Recurrence/Days` and the item's `Desc`, one lock, one save), `func (s *Store) deleteInbox(id int) error`.

- [ ] **Step 1: Write failing tests** with `newTestStore` + `httptest.NewServer(newDaemonMux(...))`:
  - `TestHabitsRoute`: add daily task "read" → `GET /habits` returns 1 habit, `name == "read"`.
  - `TestInboxScheduleRoute`: `store.addInbox("call plumber","cli")`, `POST /inbox/{id}/schedule {"text":"every weekday at 15:00"}` → 200; `GET /inbox` empty; one task with `Desc "call plumber"`, `Time "15:00"`, `Recurrence weekdays`.
  - `TestInboxScheduleNoTime`: text `"someday"` → 422; `GET /inbox` still has the item.
  - `TestInboxDeleteRoute`: delete → 200 and inbox empty; delete again → 404.
  - `TestInboxEmptyIsArray`: fresh store, `GET /inbox` body is `[]`.
- [ ] **Step 2:** `go test -run 'Habits|Inbox' ./...` → FAIL (404 routes).
- [ ] **Step 3:** Add the store methods, handlers and mux lines. Parse with `DefaultParse(body.Text, time.Now())`.
- [ ] **Step 4:** Same command → PASS; `go test ./... -race` → PASS.
- [ ] **Step 5:** Commit with `/ledger`.

### Task 3: Today grouping

**Files:**
- Create: `tui_panes.go`
- Test: `tui_test.go`

**Interfaces:**
- Produces:
  ```go
  type TodayGroups struct{ Overdue, Next, Later, Done []Task }
  func groupToday(tasks []Task, now time.Time) TodayGroups
  ```
  - Only tasks with `shouldFireTodayAt(t, now)`.
  - Done = `Done || Dismissed`. Overdue = not done and `timeUntilAt(t, now) < 0`. Next = first upcoming (at most one). Later = the rest. Each group in time order.

- [ ] **Step 1: Failing test** `TestGroupToday`: now 13:30; tasks 13:00 pending, 15:00 pending, 16:30 pending, 10:00 done, 11:30 dismissed, a weekend-only task (now is Friday) → Overdue `[13:00]`, Next `[15:00]`, Later `[16:30]`, Done `[10:00, 11:30]`, weekend task absent.
- [ ] **Step 2:** `go test -run TestGroupToday ./...` → FAIL.
- [ ] **Step 3:** Implement in `tui_panes.go`.
- [ ] **Step 4:** PASS; full suite PASS.
- [ ] **Step 5:** Commit with `/ledger`.

### Task 4: Pane layout, focus and sync

**Files:**
- Modify: `tui.go` (model, `Update`, `View`, `handleNormal`, `syncCmd`, `applySync`), `tui_panes.go` (render functions)
- Test: `tui_test.go`

**Interfaces:**
- Consumes: `groupToday` (Task 3), `Habit` (Task 1), `GET /inbox`, `GET /habits` (Task 2).
- Produces:
  ```go
  type pane int
  const (paneToday pane = iota; paneHabits; paneInbox)
  // model gains: focus pane; inbox []Task; habits []Habit; doneOpen bool; todayCursor, habitCursor, inboxCursor int
  // syncMsg gains: inbox []Task; habits []Habit
  ```

Layout (from spec): header line (name, date·time, daemon dot green/red); Today pane left ~45% width; right column Habits (top) and Inbox (bottom); footer = quick-add placeholder line + at most 6 keys for the focused pane. Alarm bar and offline banner keep their current text and sit under the header.

Keys: `tab`/`shift+tab` cycle focus; `j/k/g/G` move in the focused pane; Today: `enter` done (on the DONE row: toggle `doneOpen`), `s` snooze, `e` edit, `d` delete; Habits: `enter` focuses Today on the habit's next open slot; `q` quit. Alarm keys unchanged.

Remove: `calSuggestMode`, `calTimelineMode`, `calEvents`, `calSelected`, `calCursor`, `calLoading`, `calError`, `habitView`, `habitFull`, `fetchCalEventsCmd`, `importEventsCmd`, `handleCalSuggestions`, `renderCalSuggestions`, `renderTimeline`, `parseHour`, `renderHabits`, the `a`–`i` quick-fill, the `p`/`r`/`h`/`f` cases, and the old table in `View`.

- [ ] **Step 1: Failing tests:**
  - `TestTabCyclesFocus`: `tab` ×3 → Habits, Inbox, Today; `shift+tab` → Inbox.
  - `TestOfflineBlocksDone`: model with `offline: true`, one overdue task, focus Today, key `enter` → returned cmd is nil and `m.msg == "daemon down"`.
  - `TestEmptyStoreRenders`: empty model, width 120 height 40 → `View()` contains "No tasks today", "No habits yet", "Inbox empty"; keys `j`,`k`,`enter`,`tab` do not panic.
  - `TestViewShowsGroups`: one overdue and one upcoming task → `View()` contains "OVERDUE" and "NEXT", not "LATER".
- [ ] **Step 2:** `go test -run 'Tab|Offline|Empty|ViewShows' ./...` → FAIL.
- [ ] **Step 3:** Implement. Delete the removed code in the same step.
- [ ] **Step 4:** PASS; `go test ./... -race` PASS; `go vet ./...` clean.
- [ ] **Step 5:** Run `./flow-breaker` against the live daemon; check the layout at your terminal width.
- [ ] **Step 6:** Commit with `/ledger`.

### Task 5: Quick-add bar and inbox actions

**Files:**
- Modify: `tui.go`, `tui_panes.go`
- Test: `tui_test.go`

**Interfaces:**
- Consumes: `POST /capture` (`{"text","source":"tui"}` → `{"echo": string}`), `POST /inbox/{id}/schedule`, `DELETE /inbox/{id}` (Task 2), `taskActionCmd`.
- Produces: model gains `barMode int` (0 none, 1 add, 2 schedule) and `barText string`.

Behavior:
- `/` or `a`: bar mode add. `enter` sends `/capture` and shows the daemon's `echo` as the message. `esc` cancels. Paste appends.
- `ctrl+e` in add mode: open the current add form (`inputMode`) unchanged.
- Inbox `enter`: bar mode schedule, prompt "when? (e.g. every weekday at 9:30)". `enter` sends schedule; a 422 shows "no time found — try 'at 15:00'".
- Inbox `d`: y/n confirm, then `DELETE /inbox/{id}`.
- Placeholder: `add: "standup every weekday at 9:30"`.

- [ ] **Step 1: Failing tests:**
  - `TestSlashOpensBar`: `/` → `barMode == 1`; type "abc" → `barText == "abc"`; `esc` → `barMode == 0`.
  - `TestCaptureEchoShown`: `handleActionResult(actionMsg{ok: "📥 inbox — x"})` → `m.msg == "📥 inbox — x"`.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement. Make `/capture` a cmd that decodes `echo` into `actionMsg.ok`.
- [ ] **Step 4:** PASS; full suite PASS.
- [ ] **Step 5:** Commit with `/ledger`.

### Task 6: `?` overlay and README

**Files:**
- Modify: `tui.go`, `README.md` (TUI keybindings section)
- Test: `tui_test.go`

Behavior: `?` opens an overlay listing all keys, with entries `v` Archive, `f` Calendar feeds, `o` Settings. These keys work only inside the overlay; each opens the current screen as before, `esc` returns. Remove `v`, `o`, `f` from the main screen.

- [ ] **Step 1: Failing test** `TestHelpOverlayRoutes`: `?` → help open; `o` → `settingsMode`; `esc` → main. On main screen, `o` alone does nothing.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement. Rewrite README "TUI keybindings" to match (remove Calendar mode and habit view sections).
- [ ] **Step 4:** PASS; `go test ./... -race` PASS.
- [ ] **Step 5:** Commit with `/ledger`.
