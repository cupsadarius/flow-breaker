# flow-breaker

Terminal daily planner that breaks your flow — with alerts, habit tracking, and a socket API.

<!-- screenshot -->

## Features

- **TUI planner** built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [Lip Gloss](https://github.com/charmbracelet/lipgloss)
- **6 independent alert types** — notification, modal dialog, text-to-speech, system sound, terminal bell, tmux pane flash
- **Habit tracker** — inline weekly dots per task, full-history grid view
- **Unix socket API** — query status, nudge, next task, overdue list, alarm state from any script
- **Status file** — JSON updated every 500ms while the TUI runs
- **CLI commands** — add, list, done, clear, status, nudge without launching the TUI
- **Recurrence** — once, daily, weekdays, weekly, monthly, custom day picker
- **Tags** — comma-separated, displayed inline
- **Snooze** — configurable duration (1–60 min), per-task snooze state
- **Calendar integration** — subscribe to iCal feeds (HTTP or local `.ics` files), view today's events in a timeline, import events as one-off tasks
- **Claude Code integration** — `flow-breaker nudge` returns a one-liner suitable for LLM context

## Install

```bash
go install github.com/cupsadarius/flow-breaker@latest
```

Or build from source:

```bash
git clone https://github.com/cupsadarius/flow-breaker.git
cd flow-breaker
go build -o flow-breaker .
```

## Quick start

```bash
# Launch the TUI
flow-breaker

# Add a task from CLI
flow-breaker add 09:00 "Stand-up" --repeat weekdays
flow-breaker add 12:30 "Lunch" --repeat daily --tags health
flow-breaker add 14:00 "Call plumber" --repeat once
```

## CLI commands

| Command | Description |
|---------|-------------|
| `flow-breaker` | Launch TUI |
| `flow-breaker add <HH:MM> <desc> [flags]` | Add a task |
| `flow-breaker list` / `ls` | List today's tasks |
| `flow-breaker done <HH:MM or substring>` | Mark a task done (matches by time or description) |
| `flow-breaker clear` | Delete all tasks |
| `flow-breaker status` | Print JSON status report |
| `flow-breaker nudge` | One-liner for scripts / Claude Code |
| `flow-breaker cal-add <url\|path> [--label "X"]` | Add an iCal feed (HTTP URL or local `.ics` file) |
| `flow-breaker cal-remove <url\|label>` | Remove a feed by URL or label substring |
| `flow-breaker cal-feeds` | List configured feeds |
| `flow-breaker cal-list` | Show today's calendar events |
| `flow-breaker claude-install` | Install Claude Code hook + instructions |
| `flow-breaker help` | Print usage |

### `add` flags

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--repeat <type>` | `-r` | Recurrence: `once`, `daily`, `weekdays`, `weekly`, `monthly` | `daily` |
| `--tags <a,b>` | `-t` | Comma-separated tags | none |

## TUI keybindings

### Normal mode

| Key | Action |
|-----|--------|
| `a` | Add task |
| `e` | Edit selected task |
| `d` / `x` | Delete selected task (confirms with y/n) |
| `c` | Toggle done |
| `h` | Toggle habit tracker view |
| `p` | Calendar events (import / timeline) |
| `f` | Manage calendar feeds |
| `o` | Open settings |
| `r` | Reload tasks from disk |
| `j` / `down` | Move cursor down |
| `k` / `up` | Move cursor up |
| `g` | Jump to first task |
| `G` | Jump to last task |
| `q` / `ctrl+c` | Quit |

### Alarm mode

When an alarm fires, the TUI shows a flashing bar and only these keys work:

| Key | Action |
|-----|--------|
| `space` / `enter` | Dismiss alarm |
| `s` | Snooze (configurable minutes) |
| `c` | Mark done and dismiss |

### Add / Edit mode

Fields are filled in sequence: Time -> Description -> Recurrence -> Days (if applicable) -> Tags -> Confirm.

| Key | Action |
|-----|--------|
| `enter` | Advance to next field / confirm |
| `esc` | Cancel |
| `j` / `k` | Navigate recurrence options |
| `h` / `l` | Navigate day picker |
| `space` | Toggle day on/off (day picker) / select recurrence |
| `y` / `enter` | Confirm at final step |
| `n` | Cancel at final step |

### Settings mode

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate settings |
| `space` / `enter` | Toggle alert on/off |
| `h` / `l` (left/right) | Adjust snooze duration (1–60 min) |
| `esc` / `o` | Save and close settings |
| `q` | Quit |

### Calendar mode

Press `p` to open the calendar event picker (requires calendar to be enabled with at least one feed).

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate events |
| `space` | Toggle event selection |
| `a` | Select all |
| `enter` | Import selected events as tasks (tagged `gcal`) |
| `t` | Toggle timeline view |
| `r` | Refresh events (bypasses cache) |
| `f` | Switch to feed management |
| `esc` | Close |

### Feed management mode

Press `f` to manage iCal feeds.

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate feeds |
| `a` | Add a new feed (enter URL, then label) |
| `d` | Delete selected feed (confirms with y/n) |
| `esc` | Close |

## Habit tracker

Press `h` in the TUI to toggle the habit tracker view. It shows recurring tasks only with a dot grid:

- **Weekly view** (default) — Mon through Sun of the current week
- **Full history** — press `f` to expand to as many days as the terminal width allows

Each cell shows:
- `██` (green) — completed
- `··` (yellow) — dismissed
- `··` (red) — missed
- blank — future day

The main task list also shows inline weekly dots in the rightmost column.

## Alert system

Six alert types fire independently when a task becomes due. Toggle each on/off in settings (`o`):

| Alert | What it does | Requirement |
|-------|-------------|-------------|
| **Notification** | macOS Notification Center with "Glass" sound | macOS + `osascript` |
| **Modal dialog** | AppleScript alert with Snooze/Done/Dismiss buttons | macOS + `osascript` |
| **Text-to-speech** | `say -v Samantha` reads the task description | macOS |
| **System sound** | Plays "Funk" system sound via `afplay` | macOS |
| **Terminal bell** | Prints `\a` (BEL character) | Any terminal |
| **Tmux flash** | Displays message + flashes pane red for 3 seconds | tmux session |

While an alarm is active the TUI also plays a "Ping" sound every ~5 seconds.

The modal dialog is non-blocking — its result (Snooze/Done/Dismiss) is captured asynchronously and applied to the TUI state.

## Calendar integration

Subscribe to iCal feeds to see today's events alongside your tasks. Supports HTTP URLs (Google Calendar, Outlook, etc.) and local `.ics` files.

```bash
# Add a remote iCal feed
flow-breaker cal-add "https://calendar.google.com/calendar/ical/.../basic.ics" --label "Work"

# Add a local .ics file
flow-breaker cal-add ~/calendars/personal.ics --label "Personal"

# View today's events
flow-breaker cal-list

# Manage feeds
flow-breaker cal-feeds
flow-breaker cal-remove "Work"
```

In the TUI, press `p` to view today's events, toggle a timeline, and selectively import events as tasks. Imported events are tagged `gcal` and deduplicated on subsequent imports.

Events are cached locally (default 15 minutes, configurable in settings). Press `r` in the calendar view to force a refresh.

## Integration

### Unix socket

While the TUI is running, a Unix socket is available at `~/.flow-breaker/flow.sock`. Send a command and read the response:

```bash
echo "status" | nc -U ~/.flow-breaker/flow.sock
echo "nudge"  | nc -U ~/.flow-breaker/flow.sock
echo "next"   | nc -U ~/.flow-breaker/flow.sock
echo "overdue"| nc -U ~/.flow-breaker/flow.sock
echo "alarm"  | nc -U ~/.flow-breaker/flow.sock
echo "calendar"| nc -U ~/.flow-breaker/flow.sock
```

| Command | Response |
|---------|----------|
| `status` | Full JSON status report |
| `nudge` | One-line human-readable summary |
| `next` | JSON of next upcoming task |
| `overdue` | JSON array of overdue tasks |
| `alarm` | JSON of alarm task, or `false` if no alarm |
| `calendar` | JSON array of today's calendar events (requires calendar enabled) |

### Status file

`~/.flow-breaker/status.json` is written every 500ms while the TUI runs. Structure:

```json
{
  "timestamp": "2025-03-18T10:30:00Z",
  "alarm_firing": false,
  "next": { "time": "11:00", "desc": "Stand-up", "time_until": "29m 45s", "seconds": 1785 },
  "overdue": [],
  "upcoming": [],
  "done": [],
  "nudge": "Next: 11:00 Stand-up (29m 45s away)"
}
```

### Claude Code setup

```bash
# One-command install — adds SessionStart hook + instructions to ~/.claude/
flow-breaker claude-install
```

This adds a `SessionStart` hook so Claude Code runs `flow-breaker nudge` at the start of every conversation, and appends instructions to `~/.claude/CLAUDE.md` telling Claude to surface alerts. Safe to run multiple times (idempotent).

### Claude Code usage

```bash
# In a Claude Code hook or prompt:
flow-breaker nudge

# Or read the status file:
cat ~/.flow-breaker/status.json | jq .nudge
```

`nudge` returns context-aware one-liners:
- `"🚨 ALARM: 09:00 — Stand-up (do it NOW)"`
- `"⚠️ OVERDUE: Stand-up (09:00)"`
- `"⏰ In 4m 30s: Stand-up — wrap up what you're doing"` (within 10 minutes)
- `"Next: 11:00 Stand-up (29m 45s away)"`
- `"✅ All clear — no more tasks today"`

The `nudge` CLI command tries the live socket first (for alarm state), falls back to the status file, and finally computes from the task file if nothing else is available.

## Configuration

### Data directory

Default: `~/.flow-breaker/`

Override with `FLOW_BREAKER_DIR`:

```bash
export FLOW_BREAKER_DIR=~/my-planner
```

### Files

| File | Purpose |
|------|---------|
| `tasks.json` | All tasks, settings, reset state |
| `status.json` | Live status (written by TUI every 500ms) |
| `flow.sock` | Unix socket (created by TUI, cleaned up on exit) |
| `calendar_feeds.json` | Configured iCal feed URLs and labels |
| `calendar_cache.json` | Cached calendar events for today |

### Settings

Settings are stored inside `tasks.json` and edited via the TUI settings screen (`o`):

| Setting | Default | Range |
|---------|---------|-------|
| Snooze duration | 5 min | 1–60 min |
| Notification | on | on/off |
| Modal dialog | on | on/off |
| Text-to-speech | on | on/off |
| System sound | on | on/off |
| Terminal bell | on | on/off |
| Tmux flash | on | on/off |
| Calendar enabled | off | on/off |
| Calendar cache | 15 min | minutes |

## Recurrence types

| Type | Fires on |
|------|----------|
| `once` | Every day (until manually deleted) |
| `daily` | Every day |
| `weekdays` | Monday through Friday |
| `weekly` | Every day (use custom days for specific weekdays) |
| `monthly` | Every day (use custom days for specific weekdays) |
| `custom` | Selected days via the day picker (Mon–Sun) |

For `weekdays`, `weekly`, and `custom` recurrence, the day picker lets you select specific days.

## License

MIT
