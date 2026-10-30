# Mac calendar sync

## Intent

Calendar import is clumsy today. The user downloads an `.ics` file and adds it as a feed
(`~/Downloads/darius@optura.ai.ics`). The feed only changes when the user downloads it again.
The user wants events to come from macOS Calendar, which syncs the Google account by itself.

## Goals

- Today's events from macOS Calendar reach the Pi daemon without manual steps.
- A calendar change reaches the daemon within about a minute.
- One macOS permission prompt, one time.
- The TUI shows when sync is stale.

## Non-goals

- Google Calendar API or OAuth.
- Two-way sync. flow-breaker never writes to the calendar.
- All-day events as tasks. They stay excluded, as today.

## Anti-goals

- A launchd agent. SentinelOne killed launchd processes that send data out before. The user rejected launchd.
- Code that hides from or works around SentinelOne. If SentinelOne kills the helper, the fix is an exclusion from IT.
- Silent failure. A stale sync must show in the TUI and the nudge.

## Facts that shape the design

- The daemon runs on a Raspberry Pi (`https://rpi.tailec397d.ts.net`, systemd user unit). It cannot read macOS Calendar.
- The Mac is a client. It must read the events and send them to the daemon.
- macOS gives calendar access to an app bundle, not to a script. A helper started from a terminal asks for access in the terminal's name, once per terminal app.
- `swift` is installed at `/usr/bin/swift`.
- The daemon already turns calendar events into one-off tasks with `Store.materializeCalendar(events, now)`, with `CalUID` as the upsert key.

## Design

### Helper app

- `FlowBreakerSync.app`, installed in `~/Applications`.
- Swift, EventKit. `LSUIElement = true`: no window, no dock icon.
- `Info.plist` has `NSCalendarsFullAccessUsageDescription`.
- The source lives in this repo (for example `macos/FlowBreakerSync/`).
- `flow-breaker cal-sync --install` builds the app with `swiftc`, writes the bundle, and ad-hoc signs it (`codesign -s -`).
- An ad-hoc signature ties the calendar grant to one build. Each `--install` costs one new prompt; that is accepted (Claude's call).

### Start: lazy, from flow-breaker

- The TUI, CLI commands and `flow-breaker nudge` run `open -g -j ~/Applications/FlowBreakerSync.app` on macOS.
- `open` on a running app does nothing, so macOS gives single-instance for free. No pid or lock file.
- The call does not wait. The nudge must stay inside its 800 ms hook timeout.
- If the app is not installed, do nothing. Do not show an error on every call.
- Only on macOS (`runtime.GOOS == "darwin"`).

### Sync loop, inside the helper

- Sync at start, on `EKEventStoreChanged`, and every 15 minutes. The 15-minute tick also loads the new day after midnight.
- Each sync reads today's timed events and sends them all. `materializeCalendar` is an idempotent upsert, so a repeat send changes nothing.
- After each attempt, it writes a status file in the flow-breaker data dir (`~/.flow-breaker/calsync.json`): last attempt time, last success time, event count, last error.

### Daemon route

- `POST /calendar/events` with today's events in the `CalendarEvent` shape (UID, summary, start, end, all-day, declined, source label).
- Declined and cancelled events stay excluded, as with feeds today. The helper sets `declined` from the current user's `EKParticipant.participantStatus`, and leaves out cancelled events (`EKEventStatus.canceled`).
- No auth, the same as every other daemon route on the tailnet. The client config holds only a URL.
- The daemon validates the payload and calls `materializeCalendar`.
- The response returns the added, updated and dropped counts.
- Pushed events and feed events go through the same function.
  - **Builder's call:** how the two sources share `materializeCalendar` without one dropping the other's events.
  - The user plans to remove the file feed after this works.

### Daemon address and auth

The helper uses the same daemon URL and auth as the Go client (`~/.config/flow-breaker/client`, `newClient`). It does not keep a second copy of the config.
- **Builder's call:** read the config file directly in Swift, or have the helper call `flow-breaker` to send.

### Calendar selection

- Sync all calendars of the account that holds `darius@optura.ai`. If that account does not exist, sync all calendars.
- No picker. Add one when a second account needs it.

### Staleness

- The TUI header and the **loud** `flow-breaker nudge` read `calsync.json`. The quiet nudge never shows it (Claude's call: it runs every turn).
- If the last success is older than 30 minutes, show "calendar sync stale since HH:MM".
- No false alarm after sleep: a sync counts as stale only when the last attempt is also older than 5 minutes. A helper that just started has a fresh attempt.
- If calendar access is denied, show "calendar access denied — System Settings → Privacy → Calendars".

### TUI

The Feeds overlay becomes a Calendar overlay. It shows:
- the synced calendars,
- the last sync time and today's event count,
- any error from `calsync.json`,
- the iCal feeds, which still work as a fallback.

## Setup the user does once

1. System Settings → Internet Accounts → add the Google account with Calendars on.
2. Run `flow-breaker cal-sync --install`.
3. Accept the calendar prompt.
4. Run `flow-breaker cal-remove "Optura AI"` to remove the downloaded file feed.

## Constraints

- No launchd, no login item.
- No new Go dependency.
- Swift and EventKit only on the helper side.
- `make test` must not need macOS Calendar access. Test the Go side (route, staleness logic) with fakes.

## Left to the builder

- Swift project layout and build flags.
- Retry details.
- Status file format, beyond the fields listed.
