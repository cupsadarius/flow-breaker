# flow-breaker v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move flow-breaker from a local macOS TUI to a Pi-hosted daemon that captures tasks from anywhere over HTTP and pushes alerts to the phone, with heuristics+Haiku parsing, an escalation state machine, and agent (Claude Code / opencode / Codex) integration.

**Architecture:** One Go binary, two modes. `flow-breaker daemon` is the headless Pi server — sole writer of `tasks.json`, owner of all schedulers/tickers, and the single HTTP/JSON API. `flow-breaker` (no subcommand) is a pure HTTP client (TUI + CLI subcommands) pointing at the daemon. The unix socket and TUI-hosted-server model are retired. TLS/auth is delegated to `tailscale serve` on the tailnet; no tokens.

**Tech Stack:** Go (stdlib `net/http`, `encoding/json`, no CGO), Bubble Tea (TUI), systemd `--user` unit on Raspberry Pi (arm64 cross-compile), `tailscale serve`, self-hosted ntfy, Telegram Bot API, Anthropic Haiku (`claude-haiku-4-5`) for parser fallback.

**Source of truth:** design doc `v2.md`; wayfinder map [issue #1](https://github.com/cupsadarius/flow-breaker/issues/1) and its 12 resolved decision tickets T1–T12 (#2–#13).

**Guardrail (from `v2.md`):** "a handful of small features, not a v2 rewrite." Be suspicious of any task that balloons into a three-week project.

---

## Baseline (current code at HEAD `44480d5`)

- `main.go` — CLI dispatch, else runs TUI; TUI calls `startSocketServer` + `writeStatusFile`.
- `store.go` — `Task`, `Settings`, `Store` (value type, **no mutex**); `loadStore` returns `Store` by value; `addTask/editTask/deleteTask`, `resetDaily`, calendar import, time helpers. `dataPath` / `statusPath` / `sockPath` all under `~/.flow-breaker/`.
- `integration.go` — `StatusReport`, `buildStatus`, `writeStatusFile`, unix socket server (`startSocketServer`/`handleConn`) with **read-only** commands `status/nudge/next/overdue/alarm/calendar`.
- `alert.go` — `alertAll` (6 macOS alert types), `alarmState{active,taskIdx,tick,snoozeUntil}` with `trigger/dismiss/snooze`.
- `calendar.go` — iCal feed store, `getCachedOrFetchEvents` (lazy on-read TTL fetch).
- `cli.go` — CLI subcommands (currently read via socket / direct store).
- `tui.go` — Bubble Tea model (~1975 lines).

> **NOTE:** the working tree currently holds ~717 uncommitted lines implementing the *older socket-watch* spec (`docs/superpowers/specs/2026-04-01-socket-write-watch-skill-design.md`). That effort is superseded by v2 (T6 retires the socket) and is **discarded in Phase 0, Task 0**. All line references above are HEAD, not the working tree.

---

## Phase map & dependency order

Follows `v2.md`'s build order; each phase is independently buildable, testable, and committable.

| Phase | Ticket(s) | What ships | Depends on |
|---|---|---|---|
| **0. Reset & scaffold** | — | Discard socket-watch WIP, add `Store` mutex + `findTaskByID`, `daemon` subcommand stub, HTTP test harness | — |
| **1. Daemon + unified HTTP API** | T1 (#2) | `flow-breaker daemon` serves all read+write routes; scheduler/tickers move server-side | 0 |
| **2. TUI/CLI as pure HTTP client** | T6 (#7) | TUI polls `GET /status`; CLI mutates via HTTP; socket deleted; offline red banner | 1 |
| **3. Pi deploy & persistence** | T3 (#4) | `make deploy`, atomic `tasks.json` write, tmpfs `status.json`, systemd `--user` unit, TZ | 1 |
| **4. Parser + Haiku fallback** | T2 (#3) | heuristics-first parser → Haiku fallback → inbox floor | 1 |
| **5. Capture endpoint** | T7 (#8) | `POST /capture` wired to the parser pipeline, always-200, structured echo | 1, 4 |
| **6. Alerts-out + escalation** | T9 (#10), T4 (#5) | ntfy 7th alert type, per-taskID escalation ladder, Telegram fallback, calendar-as-source | 1, 3 |
| **7. Calendar refresh** | T5 (#6) | daemon-side periodic refresh + secret-URL helper | 1 |
| **8. Client shortcuts setup** | T8 (#9) | device-setup runbook (Android/Mac capture + ntfy), fail-loud floor | 3, 5 |
| **9. Claude Code integration** | T10 (#11) | `nudge` over HTTP, honest unreachable, hooks, granular fail-closed perms | 2, 5 |
| **10. opencode parity** | T11 (#12) | port T10 to opencode | 9 |
| **11. Codex parity** | T12 (#13) | port T10 to Codex CLI | 9 |

Phases 3, 4, 7 can run in parallel with each other once Phase 1 lands. Phases 9→10→11 are sequential (11/10 port 9's model).

> **Deliberate divergence from `v2.md`'s build order:** the doc lists "ntfy out" (its step 2) before "capture" (step 3). This plan ships parser+capture (Phases 4–5) before alerts+escalation (Phase 6). Reason: capture is self-contained and lets real tasks accumulate for the escalation machine to act on, and Phase 6 already depends on Phase 3 (tmpfs/persistence) regardless. Not a dependency violation — just a sequencing choice. If you'd rather feel the "alerts reach the phone" win first, Phase 6 can be pulled ahead of 4–5 (both only depend on Phases 1 & 3).

---

## Phase 0 — Reset & scaffold

**Goal:** Clean baseline + the shared primitives every later phase needs (thread-safe store, ID lookup, daemon subcommand entrypoint, an HTTP test harness).

**Files:**
- Modify: `store.go` (add `sync.RWMutex`, `findTaskByID`)
- Create: `daemon.go` (subcommand stub + `http.ServeMux` wiring)
- Modify: `main.go` (dispatch `daemon` subcommand)
- Test: `daemon_test.go`

- [ ] **Task 0: Discard the socket-watch WIP**

```bash
git status                       # confirm the 5 modified files are the socket-watch WIP
git checkout -- cli.go integration.go main.go store.go tui.go
git status                       # working tree clean except untracked docs/, CLAUDE.md, v2.md, this plan
```
Expected: `git diff` empty for tracked `.go` files. **This is irreversible — confirm the diff is the socket-watch WIP before running.**

- [ ] **Task 1: Add a write lock to `Store`**

`store.go`: add `mu sync.RWMutex` to `Store`. **Go's `RWMutex` is NOT reentrant** — and `addTask`/`editTask`/`deleteTask`/`resetDaily` all call `s.save()` internally, so locking in *both* the mutation method and `save()` self-deadlocks. Correct shape:
- Split `save()` into a public `save()` (takes `mu.Lock()`) and an unexported `saveLocked()` (no lock, assumes caller holds it).
- Exported mutation methods (`addTask`/`editTask`/`deleteTask`/`resetDaily`) take `mu.Lock()` once at the top and call `saveLocked()`.
- Read paths (`buildStatus`/`ActiveTasks`/`ArchivedTasks`) take `mu.RLock()`.

Note: `encoding/json` ignores the unexported `mu` field automatically. Verify no lock is taken twice on any single call path (`go test -race` in Phase 1 Task 7 would hang, not race, if it is).

- [ ] **Task 2: Write the failing test for `findTaskByID`**

`daemon_test.go` (or `store_test.go`):
```go
func TestFindTaskByID(t *testing.T) {
	s := &Store{Tasks: []Task{{ID: 1}, {ID: 7}}, NextID: 8}
	if _, err := s.findTaskByID(7); err != nil {
		t.Fatalf("want task 7, got err %v", err)
	}
	if _, err := s.findTaskByID(99); err == nil {
		t.Fatal("want error for missing id 99")
	}
}
```
Run: `go test ./... -run TestFindTaskByID -v` → FAIL (undefined `findTaskByID`).

- [ ] **Task 3: Implement `findTaskByID`**

`store.go`:
```go
// findTaskByID returns the index of the task with the given id, or an error.
func (s *Store) findTaskByID(id int) (int, error) {
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("task %d not found", id)
}
```
Run test → PASS.

- [ ] **Task 4: `loadStore` returns `*Store`**

Change `loadStore() Store` → `loadStore() *Store` (a mutex must not be copied). Update all callers (`main.go`, `cli.go`, `tui.go`). Run `go build ./...` → PASS.

- [ ] **Task 5: `daemon` subcommand stub + `/health`**

`daemon.go`:
```go
func runDaemon() {
	store := loadStore()
	store.resetDaily()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	addr := daemonAddr() // env FLOW_BREAKER_ADDR else "127.0.0.1:7777" (loopback only — never bind the LAN; tailscale serve proxies tailnet→localhost)
	log.Printf("flow-breaker daemon on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
```
`main.go`: add `case "daemon": runDaemon()`. (Go 1.22+ method-pattern routes `"GET /health"` — confirm `go.mod` Go version ≥ 1.22; bump if needed.)

- [ ] **Task 6: Failing test → passing for `/health`**

`daemon_test.go`: spin the mux via `httptest.NewServer`, `GET /health`, assert 200 + body `ok`. Run → PASS.

- [ ] **Task 7: Commit**

```bash
git add store.go daemon.go main.go daemon_test.go
git commit -m "feat: daemon scaffold, store mutex, findTaskByID"
```

---

## Phase 1 — Daemon + unified HTTP API (T1 #2)

**Goal:** `flow-breaker daemon` serves every read + write route over HTTP/JSON. The scheduler/alarm tickers move server-side. Every mutation records a `source`.

**Files:**
- Modify: `daemon.go` (route handlers), `store.go` (`Task.Source` field, `source` on mutations)
- Create: `daemon_routes.go` (handlers) — or keep in `daemon.go` if small
- Test: `daemon_test.go`

**Route surface (T1):**

| Route | Method | Body | Response |
|---|---|---|---|
| `/health` | GET | — | `ok` |
| `/status` | GET | — | `StatusReport` (polled ~1s by TUI) |
| `/tasks` | GET | — | `[]Task` (list) |
| `/capture` | POST | `{text, source?}` | structured echo (Phase 5) |
| `/nudge?quiet=1` | GET | — | text one-liner(s); empty when quiet-gate passes (Phase 9) |
| `/task` | POST | `{time, desc, recurrence, days?, tags?, source}` | `{ok, task}` |
| `/task/{id}/done` | POST | `{source}` | `{ok, task}` |
| `/task/{id}/snooze` | POST | `{source, minutes?}` | `{ok, task}` |
| `/task/{id}` | POST | `{fields…, source}` | `{ok, task}` (edit) |
| `/task/{id}` | DELETE | `{source}` | `{ok}` |

- [ ] **Task 1: Add `Source` to `Task` + a mutation envelope**

`store.go`: add `Source string \`json:"source,omitempty"\`` to `Task`. Define allowed values `tui`/`cli`/`ntfy`/`claude-code`/`opencode`/`codex`/`phone`/`api`/`calendar`. Add a helper to stamp source on add/edit/done/snooze.

- [ ] **Task 2: Failing test — `POST /task` adds a task**

`daemon_test.go`: `POST /task` with `{"time":"14:30","desc":"call plumber","recurrence":"once","source":"api"}` → assert 200, response `{ok:true, task:{id:…, source:"api"}}`, and the store now contains it. Run → FAIL.

- [ ] **Task 3: Implement the add handler**

`daemon.go`: decode body → `store.addTask(...)` under write lock → stamp `Source` → `writeJSON(w, map{ok, task})`. Run test → PASS.

- [ ] **Task 4: Failing test → impl for `/task/{id}/done`, `/task/{id}/snooze`, edit, delete**

One test per route, each asserting the store mutation + the `{ok,task}` / `{ok}` body + that `source` is persisted. `r.PathValue("id")` for the id (Go 1.22 routing); 404 `{error}` when `findTaskByID` fails. Implement each until green.

- [ ] **Task 5: Failing test → impl for `GET /tasks` and `GET /status`**

`/tasks` → `store.ActiveTasks()`. `/status` → `buildStatus(store, alarm)` (reuse existing `integration.go` builder — move it, don't rewrite). Assert JSON shape matches `StatusReport`. Implement → PASS.

- [ ] **Task 6: Move the scheduler/alarm ticker into the daemon**

The alarm-firing loop (currently TUI-side in `tui.go`) moves into `runDaemon` as a goroutine ticking every ~1s over `store.Tasks`, driving `alarmState`. The TUI no longer computes firing (Phase 2 makes it a viewer). Keep `alertAll` OUT of the daemon for now — the daemon decides *when*; *how* (macOS vs ntfy) is Phase 6. For this phase the daemon only maintains `alarmState` so `/status` reports `alarm_firing`.

- [ ] **Task 7: Concurrency test**

Fire N concurrent `POST /task` + `GET /status` against `httptest` server; assert no race under `go test -race`. Run → PASS.

- [ ] **Task 8: Commit**

```bash
git add -A && git commit -m "feat: unified HTTP/JSON API in daemon mode (T1)"
```

---

## Phase 2 — TUI/CLI as pure HTTP client (T6 #7)

**Goal:** TUI + CLI become pure HTTP clients. Delete the unix socket. Offline = red banner + frozen last-known, read-only.

**Files:**
- Create: `client.go` (shared HTTP client, base-URL resolution)
- Delete: `integration.go` socket bits (`startSocketServer`, `handleConn`, `sockPath`) — move `buildStatus`/`StatusReport` to daemon/shared
- Modify: `cli.go` (mutations → HTTP), `tui.go` (poll `/status`, offline banner), `main.go` (TUI no longer starts a server)
- Test: `client_test.go`

**Base-URL resolution order:** `FLOW_BREAKER_URL` env → `~/.config/flow-breaker/client` file → `http://localhost:7777`.

- [ ] **Task 1: Failing test — base-URL resolution**

`client_test.go`: set `FLOW_BREAKER_URL`, assert it wins; unset + write config file, assert file wins; neither, assert `http://localhost:7777`. Run → FAIL.

- [ ] **Task 2: Implement `client.go`**

`daemonURL() string` (resolution order above); a `Client` with `Get(path)`, `Post(path, body)`, `Delete(path, body)` returning decoded JSON or a typed `ErrDaemonDown` on dial/timeout error (2s timeout). Run test → PASS.

- [ ] **Task 3: CLI mutations over HTTP**

`cli.go`: `cliAdd`/`cliDone`/`cliSnooze`/edit/delete/`cliCapture` build the request and call `Client.Post(...)`. Remove all `net.DialTimeout("unix", sockPath(), ...)` and `cliSocketCmd`. `cliStatus`/`cliNudge` → `GET /status` / `GET /nudge`.

- [ ] **Task 4: Delete the socket server**

Remove `startSocketServer`, `handleConn`, `sockPath()`; drop the socket call in `main.go`'s TUI path and the sock-path display at `tui.go:1307`. `go build ./...` → PASS.

- [ ] **Task 5: TUI polls `/status`**

`tui.go`: replace status-file reads with a Bubble Tea `tea.Tick` (~1s) → `Client.Get("/status")` → update model. Mutating keys (add/edit/delete/snooze/dismiss, `p` import, `o` settings) → `Client.Post(...)` then refetch.

- [ ] **Task 6: Failing test → impl offline banner**

When `Client` returns `ErrDaemonDown`: render red banner exactly `⚠ daemon unreachable — stale as of HH:MM`; keep last successful fetch in model memory, each row tagged `(last known)`; **all mutations disabled** — mutating keys show `daemon down`. Never render a false empty/all-clear. Test the model reducer with an injected `ErrDaemonDown`. Run → PASS.

- [ ] **Task 7: Manual smoke + commit**

```bash
flow-breaker daemon &          # localhost:7777
flow-breaker add 14:30 "test"  # HTTP add
flow-breaker                   # TUI shows it; kill daemon → red banner
git add -A && git commit -m "feat: TUI/CLI pure HTTP client, retire unix socket (T6)"
```

---

## Phase 3 — Pi deploy & persistence (T3 #4)

**Goal:** Deployable to the Pi with atomic durable writes, tmpfs volatile writes, systemd `--user` unit, pinned timezone.

**Files:**
- Modify: `store.go` (atomic write, split runtime dir)
- Create: `Makefile` (`deploy` target), `deploy/flow-breakerd.service`, `docs/build/t3-pi-setup.md`

- [ ] **Task 1: Failing test — atomic write**

`store_test.go`: call `save()`, assert `tasks.json` exists and round-trips; assert no `.tmp` left behind. Run → FAIL (until atomic impl replaces plain `WriteFile`).

- [ ] **Task 2: Implement atomic `save()`**

`store.go`: write to `tasks.json.tmp` → `f.Sync()` → `os.Rename(tmp, dataPath())`. Applies to every mutation (sole writer, no debounce). Run test → PASS.

- [ ] **Task 3: Split runtime dir for `status.json`**

`store.go`: `statusPath()` → `$RUNTIME_DIRECTORY/status.json` if set (systemd tmpfs), else `~/.flow-breaker/status.json` (local dev). `dataPath()` unchanged. Note: with the daemon owning `/status` over HTTP (Phase 1), `status.json` is now only a debug artifact — keep it but low-priority.

- [ ] **Task 4: `Makefile` deploy target**

```makefile
deploy:
	GOOS=linux GOARCH=arm64 go build -o flow-breaker .
	scp flow-breaker pi:~/.local/bin/flow-breaker
	ssh pi 'systemctl --user restart flow-breakerd'
```

- [ ] **Task 5: systemd unit**

`deploy/flow-breakerd.service` — `--user` unit: `ExecStart=%h/.local/bin/flow-breaker daemon`, `Restart=always`, `RestartSec=2`, `RuntimeDirectory=flow-breaker`, `Environment=TZ=Europe/Bucharest`, `EnvironmentFile=%h/.config/flow-breaker/env` (holds `ANTHROPIC_API_KEY`, chmod 600). `WantedBy=default.target`.

- [ ] **Task 6: Pi setup runbook**

`docs/build/t3-pi-setup.md`: (1) `sudo timedatectl set-timezone Europe/Bucharest`; (2) `loginctl enable-linger $USER`; (3) write `~/.config/flow-breaker/env` chmod 600; (4) install unit → `systemctl --user daemon-reload && systemctl --user enable --now flow-breakerd`; (5) `make deploy` from Mac; (6) `tailscale serve --bg 7777`.

- [ ] **Task 7: Verify cross-compile + commit**

```bash
GOOS=linux GOARCH=arm64 go build -o /tmp/fb-arm64 . && echo OK
git add -A && git commit -m "feat: Pi deploy, atomic writes, tmpfs status, systemd unit (T3)"
```

---

## Phase 4 — Parser + Haiku fallback (T2 #3)

**Goal:** Turn free text into the 5-field contract. Heuristics-first (unambiguous only) → signal-gated Haiku → inbox floor. Fail loud, never silently drop.

**Pipeline:** `text → heuristics (unambiguous only) → miss? → time-ish signal? → yes: 1 Haiku call / no: inbox → Haiku unreachable|no-key|refusal: inbox + note`

**5-field contract** (emitted identically by heuristics and Haiku):

```go
type ParseResult struct {
	Time       string     // "HH:MM"
	Date       string     // "YYYY-MM-DD", today if unresolved
	Recurrence Recurrence // once/daily/weekdays/weekly/monthly (existing enum)
	Days       []string   // weekday set for weekly
	Desc       string     // stripped remainder
}
```

**Files:**
- Create: `parser.go` (heuristics + signal gate + dispatch), `haiku.go` (Anthropic call)
- Test: `parser_test.go`

- [ ] **Task 1: Failing tests for the heuristics table**

`parser_test.go` — table test, one row per pattern, EN + RO:
```go
cases := []struct{ in, time, desc string; rec Recurrence }{
	{"wash dishes in 16 min", /*now+16*/ "", "wash dishes", Once},
	{"call at 14:30",         "14:30", "call", Once},
	{"standup la 3",          "03:00", "standup", Once},   // la = "at"
	{"gym tomorrow 9",        "09:00", "gym", Once},        // date=tomorrow
	{"pills mâine 9",         "09:00", "pills", Once},
	{"review every weekday at 8", "08:00", "review", Weekdays},
	{"apă în fiecare zi",     "", "apă", Daily},            // în fiecare zi = every day
}
```
(For `in N min`, assert `time` = now+N rounded; use an injectable clock.) Run → FAIL.

- [ ] **Task 2: Implement the heuristics**

`parser.go` — `parseHeuristics(text string, now time.Time) (ParseResult, bool)`. Regexes for: `in N min|Nh` / `peste N (de )?minute`, `at HH:MM` / `la N`, `tomorrow N` / `mâine N`, `every weekday at N` / `în fiecare zi`. Match unambiguous spans only, strip span → `Desc`. **Do NOT** handle bare-hour AM/PM ("at 3") — leave for Haiku. Run tests → PASS.

- [ ] **Task 3: Failing test → impl the signal gate**

`hasTimeSignal(text) bool` → true if any digit OR any of `at,in,tomorrow,every,la,peste,mâine,în fiecare`. Test: `"call the plumber"` → false (never hits Haiku); `"call at some point 3ish"` → true. Implement → PASS.

- [ ] **Task 4: Haiku fallback**

`haiku.go` — `parseHaiku(ctx, text) (ParseResult, error)` using `anthropic-sdk-go`, model `claude-haiku-4-5`, **strict structured output** (`output_config.format` JSON schema of the 5 fields), **3s** context timeout. Key from `ANTHROPIC_API_KEY` env (systemd `EnvironmentFile`, T3). No key / HTTP error / refusal → return error (caller floors to inbox).
> **Consult the `claude-api` skill** for exact model id, pricing, structured-output param shape, and SDK usage before writing this. (~100 tok in / ~30 out ≈ $0.0003/call.)

- [ ] **Task 5: Top-level `Parse` dispatcher + inbox-floor test**

`Parse(text) (res ParseResult, dest string, parsedBy string)` where `dest ∈ {scheduled, inbox}`, `parsedBy ∈ {heuristics, haiku, none}`. Tests: heuristic hit → `scheduled/heuristics`; no signal → `inbox/none`; signal + Haiku success → `scheduled/haiku`; signal + Haiku error (mock) → `inbox/none`. Run → PASS.

- [ ] **Task 6: Commit**

```bash
git add parser.go haiku.go parser_test.go && git commit -m "feat: heuristics-first parser with Haiku fallback (T2)"
```

---

## Phase 5 — Capture endpoint (T7 #8)

**Goal:** `POST /capture` runs text through Phase 4 synchronously and returns a structured echo; always 200 on capture (inbox is a valid outcome). Introduces the separate `inbox[]` array.

**Files:**
- Modify: `store.go` (add `Inbox []Task` to `Store`; `addInbox`; daily-reset skips inbox), `daemon.go` (`POST /capture` handler)
- Test: `daemon_test.go`

**Response contract:**
```json
{ "echo": "14:12 — wash the dishes",
  "destination": "scheduled",   // or "inbox"
  "parsedBy": "heuristics",      // or "haiku"
  "task": { "id": 42, "time": "14:12", "date": "2026-08-12", "desc": "wash the dishes", "recurrence": "once" } }
```

- [ ] **Task 1: Inbox array + reset-skip**

`store.go`: add `Inbox []Task \`json:"inbox"\`` to `Store`; `addInbox(desc, source) Task` (own id from `NextID`, atomic save). Confirm `resetDaily` iterates `s.Tasks` only — **inbox is immortal**, never reset/archived. Test: add inbox item, run `resetDaily`, assert it survives.

- [ ] **Task 2: Failing test — timed capture → scheduled**

`POST /capture {"text":"wash dishes in 16 min","source":"phone"}` → 200, `destination:"scheduled"`, `parsedBy:"heuristics"`, `task.source:"phone"`, task present in `store.Tasks`. Run → FAIL.

- [ ] **Task 3: Failing test — timeless capture → inbox**

`POST /capture {"text":"call the plumber"}` → 200, `destination:"inbox"`, `parsedBy:"none"`, `source` defaults `"api"`, item in `store.Inbox`. Run → FAIL.

- [ ] **Task 4: Implement the handler**

`daemon.go`: decode `{text, source?}` (source default `api`) → `Parse(text)` → if `scheduled` `addTask` else `addInbox` → build `echo` (`"HH:MM — desc"` scheduled, `"📥 inbox — parser slow, triage later"` on Haiku-timeout floor) → 200 with the contract body. Non-2xx **only** for malformed body / parser panic. Run Tasks 2–3 → PASS.

- [ ] **Task 5: Haiku-timeout floor test**

Inject a parser whose Haiku path times out → assert 200, `destination:"inbox"`, honest echo. Confirms the "200 means captured, nothing else" promise. Run → PASS.

- [ ] **Task 6: Commit**

```bash
git add -A && git commit -m "feat: POST /capture with structured echo + inbox floor (T7)"
```

> **Deferred (map fog, do NOT build):** client-side Telegram capture tier and inbox/triage UX. Clients ship **Pi → local note** floor only. See map "Not yet specified".

---

## Phase 6 — Alerts-out + escalation state machine (T9 #10, T4 #5)

**Goal:** ntfy becomes the 7th alert type. The daemon runs an independent escalation ladder per `taskID`, falls back to Telegram at 10min, auto-snoozes at 15min, and drops exhausted alarms to a new `missed` state. Calendar events materialize into `tasks.json` as a real alarm source.

**Files:**
- Create: `ntfy.go`, `telegram.go`, `escalation.go`
- Modify: `alert.go` (ntfy as 7th type behind the interface), `store.go` (`missed` state, `Source="calendar"`, `CalUID` field, calUID-aware dedup/reset), `daemon.go` (own the escalation loop; done/snooze stop it)
- Test: `escalation_test.go`, `ntfy_test.go`, `telegram_test.go`

**Ladder (per taskID, independent):** `0:00 ntfy p3 (republish/60s) → 3:00 p4 → 6:00 p5 → 10:00 Telegram → 15:00 auto-snooze(~30m) → re-surface once → missed`.

- [ ] **Task 1: ntfy publish**

`ntfy.go`: `publishNtfy(topic, title, body string, prio int, actions []Action)` → HTTP POST to the ntfy server (URL + topic `flow` from settings/env), priority header, `Actions` header carrying **Done** → `POST /task/{id}/done` and **Snooze** → `POST /task/{id}/snooze` http actions. Test against an `httptest` stand-in ntfy; assert headers + action URLs. Add as the 7th type behind `alert.go`'s alert interface (daemon-side; macOS types stay for local `ntfy subscribe --exec`).

- [ ] **Task 2: Telegram send**

`telegram.go`: `sendTelegram(text string) error` → single stdlib `http.Post` to `https://api.telegram.org/bot<token>/sendMessage` with `{chat_id, text}`; token + chat_id from systemd env. Handle `403` (user never `/start`ed the bot) with a clear logged error. Test with an `httptest` server injected as base URL.

- [ ] **Task 3: Failing tests for the escalation state machine**

`escalation_test.go` — drive a `Ladder` with an injectable clock; assert transitions at 0/3/6/10/15 min and republish every 60s, using fake `publishNtfy`/`sendTelegram` spies:
```go
// t=0 → p3 published; t=60s → republished; t=3m → p4; t=6m → p5;
// t=10m → telegram fired once; t=15m → auto-snooze set, ntfy stops;
// re-surface ~30m later once → still unacked → state = missed, no more push.
```
Run → FAIL.

- [ ] **Task 4: Implement `escalation.go`**

Per-`taskID` `Ladder{tick, prio, republishCount, tgFired}` held in a daemon map (RAM). A daemon goroutine ticks ~1s, advances each active ladder, calls ntfy/telegram, and on the 15min terminal sets `snoozeUntil` (~30m). Re-surface once → if still unacked, set task state `missed`. Ack (`/task/{id}/done` or `/task/{id}/snooze`) removes the ladder. Escalation position is **RAM + tmpfs `status.json` mirror only — never persisted to `tasks.json`**. Run Task 3 → PASS.

- [ ] **Task 5: `missed` state + restart reconstruction**

`store.go`: add `missed` (bool or state field on `Task`), surfaced in `/status` overdue list, **no push**. On daemon start, re-arm every task that is `past-due ∧ !done ∧ !dismissed ∧ !snoozed` at **p3** (reconstruct from `tasks.json`; escalation position is disposable). Test: seed a past-due task, start daemon loop, assert a ladder exists at p3.

- [ ] **Task 6: Ack semantics wired to routes**

`/task/{id}/done` and `/task/{id}/snooze` (Phase 1 handlers) additionally tear down the ladder. Assert: tapping notification body / reading `/status` does NOT ack; only these two routes do. Test.

- [ ] **Task 7: Calendar as alarm source**

`store.go`: extend `importCalendarEvents` (or a new `materializeCalendar`) to write eligible events (**timed, non-declined, 10-min lead**) into `tasks.json` as tasks with `Source="calendar"`, allocated `id`, stored `CalUID`, `time = start − 10min`. Sync rules: dedup by `calUID`; moved event → update time; vanished → drop un-acted task; **Done'd → tombstone by `calUID`** (re-sync won't resurrect). Daily-reset + dedup are `calUID`-aware. Tests for each rule. (Refresh trigger itself is Phase 7.)

- [ ] **Task 8: Mac subscribe handler doc**

Add `deploy/alert-handler.sh` (pipes an ntfy message into existing `osascript`/`say`/`afplay`) + a note in the T8 runbook. No daemon code — Mac-side only.

- [ ] **Task 9: Commit**

```bash
git add -A && git commit -m "feat: ntfy alerts-out, per-task escalation ladder, Telegram fallback, calendar-as-source (T9/T4)"
```

---

## Phase 7 — Calendar refresh + secret-URL helper (T5 #6)

**Goal:** The daemon proactively refreshes feeds every `CalCacheMins` (default 15); clients no longer trigger fetches. `cal-add` teaches the user where to get the secret URL.

**Files:**
- Modify: `daemon.go` (refresh ticker), `cli.go` (`cal-add` helper text), `calendar.go` (move fetch off lazy read path)

- [ ] **Task 1: Daemon refresh ticker**

`daemon.go`: goroutine ticking every `Settings.CalCacheMins` (default 15) → `getCachedOrFetchEvents(feeds, ...)` for all feeds → materialize via Phase 6 Task 7. Remove the lazy on-read fetch from `cli.go`/`tui.go`/`integration.go` read paths — clients read fresh events via `/status`/`/tasks`. Cache file stays in `~/.flow-breaker/` (one write / 15 min, SD-safe).

- [ ] **Task 2: `cal-add` secret-URL helper**

`cli.go`: print the hint in **three places** — `cal-add --help`, bare `cal-add` with no arg, and once in success output:
> Google Calendar → Settings → (select the calendar) → **Integrate calendar** → **Secret address in iCal format**. Treat the URL like a password — anyone holding it can read the calendar.

Test: `cal-add` with no arg prints the hint and exits non-zero.

- [ ] **Task 3: Commit**

```bash
git add -A && git commit -m "feat: daemon-side calendar refresh + secret-URL helper (T5)"
```

---

## Phase 8 — Client shortcuts setup runbook (T8 #9)

**Goal:** A device-setup runbook (docs, not code) run **after Phase 3 deploy + Phase 5 capture are live**. The doc `docs/build/t8-client-setup.md` already exists — verify/refresh it against the shipped contracts.

**Files:** `docs/build/t8-client-setup.md` (verify), `deploy/alert-handler.sh` (from Phase 6 Task 8)

- [ ] **Task 1: Verify prerequisites section** — daemon reachable at `https://<machine>.<tailnet>.ts.net` via `tailscale serve --bg 7777`; `POST /capture` live; ntfy bound to `tailscale0` behind `tailscale serve`, topic `flow`; phone + Mac on tailnet.
- [ ] **Task 2: Android capture** — HTTP Shortcuts app, POST `/capture`, body `{"text":"{{input}}","source":"phone"}`, `Content-Type: application/json`, response-as-toast = `echo`, Gboard mic; **fail-loud:** red toast + append to local note.
- [ ] **Task 3: Mac capture** — Shortcuts.app: Dictate Text → Get Contents of URL (POST JSON, `source:"mac"`) → Show Result; global hotkey; fail-loud append to Apple Note.
- [ ] **Task 4: Android notify** — ntfy app, add server + subscribe `flow`, exempt battery optimization + instant delivery, priority 5 alarm / 3 heads-up, Done/Snooze action buttons hit the daemon.
- [ ] **Task 5: Mac notify** — `ntfy subscribe .../flow --exec alert-handler.sh` (launchd), reuses all six v1 alert types.
- [ ] **Task 6: E2e smoke checklist** — timed capture → toast echo → in `/status`; timeless → inbox; test alarm → phone priority 5 bypass DND → tap Done → done in `/status`; Mac speaks; Tailscale off → red toast + local note (nothing lost).
- [ ] **Task 7: Commit** — `git add -A && git commit -m "docs: client setup runbook + mac alert handler (T8)"`

> **Fail-loud floor:** every client ships **Pi → local note** only. Telegram capture-in tier is deferred (map fog).

---

## Phase 9 — Claude Code integration (T10 #11)

**Goal:** `flow-breaker nudge` reads over HTTP with an honest unreachable message; SessionStart is loud-once, UserPromptSubmit is quiet-gated per turn; write perms are granular and fail-closed. Establishes the client-agnostic model T11/T12 port.

**Files:**
- Modify: `cli.go` (`nudge` over HTTP, `--quiet` flag, `claude-install` perms), `daemon.go` (`GET /nudge?quiet=1` gating), CLAUDE.md template

- [ ] **Task 1: `GET /nudge` quiet gating (daemon)**

`daemon.go`: `GET /nudge` → full nudge text; `?quiet=1` → **empty body** unless interrupt-worthy (active/unacked alarm OR overdue OR due <10min). Pi owns the gate. Tests for each branch.

- [ ] **Task 2: `nudge` client + `--quiet` flag + honest unreachable**

`cli.go`: `cliNudge` → `GET /nudge` (or `?quiet=1` with `--quiet`), **800ms** timeout. Loud path (no `--quiet`): unreachable → print verbatim `⚠️ flow-breaker unreachable — tasks unknown`, **never** `✅ All clear`. Quiet path: unreachable/empty → print nothing (silent, fail-open). The `--quiet` flag is the **shared primitive** T11/T12 depend on. Test both paths with an injected down-client.

- [ ] **Task 3: `claude-install` granular perms**

`cli.go` (`nudgePerm`, ~line 402 at HEAD — grep the name, not the number): replace the current narrow `Bash(flow-breaker nudge*)` allow with:
```json
{ "allow": ["Bash(flow-breaker nudge:*)","Bash(flow-breaker status:*)","Bash(flow-breaker next:*)",
            "Bash(flow-breaker overdue:*)","Bash(flow-breaker alarm:*)","Bash(flow-breaker calendar:*)",
            "Bash(flow-breaker capture:*)","Bash(flow-breaker add:*)"],
  "ask":  ["Bash(flow-breaker done:*)","Bash(flow-breaker snooze:*)"],
  "deny": ["Bash(flow-breaker clear:*)"] }
```
Precedence deny>ask>allow; unknown subcommand falls through to default (prompt). Mutations stamp `source=claude-code`; agent calls use IDs. Test that the written settings JSON matches.

- [ ] **Task 4: CLAUDE.md template**

Update the `claude-install` CLAUDE.md text: **surface** the nudge and continue, never act on it; SessionStart loud-once + UserPromptSubmit quiet-gated; **drop** the "every 30 min / `/loop 30m`" guidance. Wire the hooks (SessionStart → `flow-breaker nudge`; UserPromptSubmit → `flow-breaker nudge --quiet`).

- [ ] **Task 5: Commit** — `git add -A && git commit -m "feat: Claude Code integration over HTTP, granular perms, quiet gating (T10)"`

---

## Phase 10 — opencode parity (T11 #12)

**Goal:** Port the T10 model to opencode. Perms via `opencode.json permission.bash` (last-match-wins); per-turn freshness via `experimental.chat.system.transform`.

**Files:** Modify `cli.go` (`opencode-install`: `patchOpencodeConfig` perms + `flow-breaker.js` plugin), AGENTS.md template

- [ ] **Task 1: `permission.bash` map**

`cli.go` (`patchOpencodeConfig`, ~line 521 at HEAD — grep the name, not the number): replace lone `flow-breaker nudge*: allow` with the full map — catch-all `flow-breaker *: ask`, then `nudge/status/next/overdue/alarm/calendar/capture/add *: allow`, then `done*/snooze*: ask`, `clear*: deny`. No global `*`. Note: Go sorted-marshal puts `flow-breaker *` (space) before letter rules → last-match-wins satisfied without ordered-map work. Test the marshalled JSON key order.

- [ ] **Task 2: `flow-breaker.js` plugin**

Init (once): inject full `flow-breaker nudge` (loud, incl. unreachable once) into system context. Per turn: run `flow-breaker nudge --quiet` inside `experimental.chat.system.transform`, **800ms** timeout, push to `output.system` **only if non-empty**, silent on unreachable/timeout. **Drop** the 30-min `setInterval`. `source=opencode`.

- [ ] **Task 3: AGENTS.md** — surface-never-act; remove any 30-min loop guidance.

- [ ] **Task 4: Commit** — `git add -A && git commit -m "feat: opencode integration parity (T11)"`

---

## Phase 11 — Codex parity (T12 #13)

**Goal:** Port the T10 model to Codex CLI. New `codex-install` command: merge hooks, write ExecPolicy rules, patch AGENTS.md.

**Files:** Modify `main.go` (`codex-install` dispatch), `cli.go` (`cliCodexInstall`), templates for `~/.codex/hooks.json`, `~/.codex/rules/flow-breaker.rules`, `~/.codex/AGENTS.md`

- [ ] **Task 1: `codex-install` command scaffold**

`main.go`: `case "codex-install": cliCodexInstall()`. `cli.go`: `cliCodexInstall()` stub.

- [ ] **Task 2: Merge `~/.codex/hooks.json`**

**Merge, do not overwrite** (supacode co-tenant). Append flow-breaker entries: `SessionStart` → `flow-breaker nudge` (loud once); `UserPromptSubmit` → `flow-breaker nudge --quiet` (gated, silent on unreachable). Hook `timeout` set generously (the 800ms budget lives inside `nudge`). Requires `[features] hooks = true`. Test that an existing hooks.json's other entries survive the merge.

- [ ] **Task 3: Write `~/.codex/rules/flow-breaker.rules`**

Dedicated file with `prefix_rule`s: base `["flow-breaker"]:prompt` (fail-closed), `nudge/status/next/overdue/alarm/calendar/capture/add: allow`, `done/snooze: prompt`, `clear: forbidden`. **Build caveat:** this repo is `trust_level=trusted` — verify `forbidden`/`prompt` hold under trusted; if execpolicy is bypassed, escalate `clear` (and done/snooze) to a `PreToolUse` hook returning allow/deny/ask. Test/verify manually with `codex` 0.147.0.

- [ ] **Task 4: `~/.codex/AGENTS.md`** — surface-never-act; no 30-min loop. `source=codex`.

- [ ] **Task 5: Commit** — `git add -A && git commit -m "feat: Codex integration parity, codex-install (T12)"`

---

## After Phase 11 — the deliberately-deferred fog

These are **not** in this plan (map "Not yet specified"). Do NOT build them speculatively:

1. **Off-tailnet capture via Telegram (ingest)** — daemon-side Telegram poll → `/capture`. Clients ship Pi→local-note until real use proves the floor too lossy.
2. **Inbox + triage UX** — TUI `i` mode (enter=promote / d=delete / t=tag), generated `Triage inbox (N)` recurring task auto-completing at 0, ~20 cap. v2.md wants this designed **after a week of real capture use**. Graduates once the daemon is deployed and used.

When either bites in real use, return to the wayfinder map, graduate it into a fresh ticket, and plan from there.

---

## Verification checklist (whole build)

- [ ] `go build ./...` and `go test ./... -race` green after every phase.
- [ ] `GOOS=linux GOARCH=arm64 go build` produces a runnable arm64 binary (no CGO).
- [ ] Daemon is the **sole writer** of `tasks.json`; no client writes it directly.
- [ ] No false `✅ All clear` anywhere a client can be offline — honest `⚠️ unreachable`.
- [ ] Every mutation persists a `source`.
- [ ] `clear` is denied for all three agents; `done`/`snooze` ask; reads+capture+add allow.
- [ ] `tasks.json` writes are atomic (temp+fsync+rename); `status.json` on tmpfs.

