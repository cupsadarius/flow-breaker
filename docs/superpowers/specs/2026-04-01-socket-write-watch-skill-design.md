# Socket Write Commands, Watch Protocol, and Claude Skill

## Context

flow-breaker's Unix socket (`~/.flow-breaker/flow.sock`) currently only supports read commands (status, nudge, next, overdue, alarm, calendar). To enable Claude to manage tasks remotely and receive real-time alarm notifications — especially for mobile remote sessions — we need:

1. **Write commands** on the socket (add, done, edit, delete, dismiss, snooze)
2. **Watch command** for long-poll alarm/event notification
3. **Claude skill** that autonomously manages tasks, monitors alarms, and surfaces notifications

## Design

### 1. Thread Safety: Store Mutex

Add `sync.RWMutex` to Store. Socket write commands run in goroutines concurrent with BubbleTea's Update loop — both access `store.Tasks` and `store.NextID`.

- Write lock: `addTask`, `editTask`, `deleteTask`, `save`, and any socket write handler
- Read lock: `buildStatus`, `writeStatusFile`, and socket read handlers
- BubbleTea's `Update` goroutine already runs single-threaded, but shares the store pointer with socket handlers

### 2. Socket Read Improvements

Replace `conn.Read(buf)` with `bufio.Reader.ReadString('\n')` in `handleConn`. Unix sockets are stream-oriented — a single `Read` can return partial data. Newline-delimited reads solve this and remove the 1024-byte buffer limit. Callers already send newlines via `echo "cmd" | nc`.

### 3. Socket Write Commands (`integration.go`)

New commands parsed in `handleConn`. All write commands acquire the store write lock, call `store.save()` and `writeStatusFile()` on success, broadcast to watchers, and return JSON responses.

**Command formats** (newline-terminated text over socket):

| Command | Format | Success Response |
|---------|--------|-----------------|
| `add` | `add HH:MM description [--repeat daily\|once\|weekdays\|weekly\|monthly] [--tags x,y] [--days Mon,Tue]` | `{"ok":true,"task":{...}}` |
| `done` | `done <id>` | `{"ok":true,"task":{...}}` |
| `edit` | `edit <id> [--time HH:MM] [--desc ...] [--repeat ...] [--tags ...] [--days ...]` | `{"ok":true,"task":{...}}` |
| `delete` | `delete <id>` | `{"ok":true}` |
| `dismiss` | `dismiss <id>` | `{"ok":true,"task":{...}}` |
| `snooze` | `snooze <id> [minutes]` | `{"ok":true,"task":{...}}` |

All errors return `{"error":"message"}`.

**Parsing rules**:
- `add`: first token after `add` is HH:MM time. Remaining tokens up to the first `--flag` are the description. Descriptions cannot contain `--`. Extract shared `parseAddArgs(args []string)` from `cliAdd()`.
- `done`/`delete`/`dismiss`/`snooze`: use numeric task ID (not description substring like CLI `done`). Add `findTaskByID(id int)` helper.
- `edit`: `--desc` consumes all tokens until the next `--flag` or end of input.

### 4. Watch Command (Long-Poll)

A `watch [timeout_seconds]` command that blocks until an event occurs or timeout expires. Default timeout: 55 seconds.

```go
type Event struct {
    Type string `json:"event"` // alarm_fired, alarm_dismissed, task_added, task_done, task_deleted, task_edited, timeout
    Task *Task  `json:"task,omitempty"`
}

type watcher struct {
    ch chan Event // buffered, size 1
}

var watchers struct {
    sync.Mutex
    list []*watcher
}
```

**`handleConn` for `watch`**:
1. Parse timeout (default 55s, max 120s)
2. Set connection deadline to `now + timeout + 5s` (buffer for writing response)
3. Create watcher, add to list
4. `select { case evt := <-w.ch: ... case <-time.After(timeout): write timeout event }`
5. Use `time.After` only for timeout control (not connection deadline)
6. Remove watcher, write event JSON, close

**`broadcastEvent` call sites** — fire only on state transitions, not every tick:
- `alarm.trigger()` in `tui.go:checkAlarms` — `broadcastEvent(Event{Type: "alarm_fired", Task: &t})`
- `alarm.dismiss()` / `alarm.snooze()` path — `broadcastEvent(Event{Type: "alarm_dismissed"})`
- Socket write commands — `broadcastEvent(Event{Type: "task_added", Task: &t})` etc.

**Note**: BubbleTea reflects socket writes on the next 500ms tick (acceptable latency — TUI re-renders from store pointer).

### 5. CLI `watch` Command

`flow-breaker watch [--timeout 55]` — connects to socket, sends `watch 55\n`, reads JSON response, prints to stdout, exits.

Exits with code 0 on event, code 0 on timeout (empty stdout). If socket unavailable, prints error to stderr and exits code 1.

### 6. Claude Skill (`~/.claude/skills/flow-breaker/SKILL.md`)

The skill must be **fully autonomous** — when loaded, Claude knows exactly what to do without any user explanation.

```yaml
---
name: flow-breaker
description: Manage flow-breaker tasks and monitor alarms. Use when the user asks to add/edit/complete/delete tasks, check schedule, or when starting a remote monitoring session. Also auto-activates at session start.
allowed-tools: Bash(flow-breaker *)
---
```

**Skill behavior**:

1. **Session start**: Run `flow-breaker nudge` to surface current status. If overdue tasks exist, tell the user immediately.

2. **Alarm monitoring**: Start a long-poll watch loop:
   - Run `flow-breaker watch --timeout 55` in background
   - On `alarm_fired` event: immediately tell user "🚨 ALARM: [task description] at [time] — what would you like to do? (done/snooze/dismiss)"
   - On `timeout`: silently re-launch watch
   - On error (socket unavailable): fall back to `flow-breaker nudge` every 60 seconds

3. **Task management** — Claude uses these commands directly:
   - Add: `flow-breaker add HH:MM description [--repeat daily] [--tags work]`
   - Done: `flow-breaker done <id>`
   - Edit: `flow-breaker edit <id> --time HH:MM --desc new description`
   - Delete: `flow-breaker delete <id>`
   - Dismiss: `flow-breaker dismiss <id>`
   - Snooze: `flow-breaker snooze <id> [minutes]`

4. **Status queries**:
   - Full status: `flow-breaker status` (JSON)
   - One-liner: `flow-breaker nudge`
   - Calendar: `flow-breaker cal-list`

5. **Fallback**: If socket commands fail, use CLI equivalents (same binary, different code path).

### 7. Update `claude-install`

Extend `cliClaudeInstall()` to:
- Write skill file to `~/.claude/skills/flow-breaker/SKILL.md`
- Broaden Bash permission from `flow-breaker nudge*` to `flow-breaker *`

## Files to Modify

| File | Changes |
|------|---------|
| `store.go` | Add `sync.RWMutex` to Store, add `findTaskByID()`, extract `parseAddArgs()` |
| `integration.go` | bufio.Reader for reads, write command handlers, watcher infra, broadcastEvent |
| `cli.go` | Use shared `parseAddArgs()`, add `cliWatch()` command, add `cliAdd/Done/Edit/Delete/Dismiss/Snooze` socket variants |
| `tui.go` | Call `broadcastEvent()` from alarm trigger and dismiss paths |
| `alert.go` | Call `broadcastEvent()` from `alarm.trigger()` and `alarm.dismiss()` |
| `main.go` | Add `watch`, `done`, `edit`, `delete`, `dismiss`, `snooze` to CLI dispatch |
| `cli.go` (claude-install) | Write skill file, broaden permission |

## Verification

1. `go build ./...` — compiles
2. `go vet ./...` — no race warnings
3. Start TUI, run `echo "add 14:00 test task\n" | nc -U ~/.flow-breaker/flow.sock` — task appears in TUI
4. Start TUI, run `flow-breaker watch --timeout 10` in another terminal, wait for alarm — event printed
5. Kill TUI, run `flow-breaker watch` — exits with clear error
6. Run `flow-breaker claude-install` — skill file created at `~/.claude/skills/flow-breaker/SKILL.md`
