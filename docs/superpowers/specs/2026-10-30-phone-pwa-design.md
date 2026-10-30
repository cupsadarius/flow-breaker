# Phone PWA

## Intent

On the phone, flow-breaker is the ntfy app for alarms plus an HTTP Shortcut to add tasks.
The user wants a proper Android client: one installed app with the task list, quick-add and
real notifications with action buttons. Body Ledger already does Web Push this way.

## Goals

- An installable PWA for Android Chrome.
- Screens: Today, Habits, Inbox, quick-add. This is a phone-sized version of the TUI variant A (`2026-10-30-tui-redesign-design.md`).
- An alarm notification with **Done** and **Snooze** buttons that act without opening the app.
- Replaces the ntfy app and the HTTP Shortcut on the phone.

## Non-goals

- iOS.
- Removing or changing the ntfy server. desk depends on it.
- Changes to the Pi daemon's alarm logic.
- Settings editing.

## Anti-goals

- The phone calling the Pi directly, or CORS.
- A second auth system. The Tailscale identity guard is the auth.
- Duplicate alarm notifications from the PWA and the ntfy app. The user removes the ntfy app from the phone once the PWA works.

## Facts that shape the design

- The Pi daemon publishes alarms to ntfy (`ntfy.go`), with Done/Snooze action URLs.
- desk (`~/Personal/desk`) already:
  - subscribes to the ntfy JSON stream (`desk/ntfy.go`) and gets the task ID from the Done action URL,
  - calls `POST /task/{id}/{done|snooze|dismiss}` on the Pi with `{"source": "desk"}`,
  - guards requests: allowed hosts, allowed `Tailscale-User-Login`, same-origin JSON on unsafe methods, strict CSP (`desk/guard.go`).
- Body Ledger (`~/Personal/body-ledger`) already does Web Push:
  - service worker `server/web/sw-template.js`, client `server/web/src/lib/push.ts`, server `server/src/health/web/push.py`,
  - VAPID keys in a 0600 file, one subscription in a JSON file, subscription deleted on 404/410, TTL 6 h.
- desk and Body Ledger run as Docker compose services on the always-on Intel Mac under OrbStack, on `127.0.0.1:<port>`, exposed with `tailscale serve --bg --https=<port>`.

## Design

### Host and stack

- A new Docker compose service on the Intel Mac, on `127.0.0.1:8091`, exposed with `tailscale serve --bg --https=8091 http://127.0.0.1:8091`.
- Go standard library server with vanilla JS, HTML and CSS embedded in the binary. No frontend build. Same pattern as desk.
- Web Push: use an existing Go Web Push library (for example `github.com/SherClockHolmes/webpush-go`). The payload encryption is security code; do not write it by hand.
- **Builder's call:** a new directory in this repo (for example `cmd/flow-breaker-web/`) or a new repo.
- The app defines its own small JSON structs for the fields it reads, like desk does. No package split of the daemon (Claude's call).
- Build after the TUI redesign. It needs the TUI work's daemon routes: `GET /inbox`, `POST /inbox/{id}/schedule`, `DELETE /inbox/{id}`, `GET /habits`.

### Data path

Phone → Mac app → Pi daemon. The Mac app proxies:
- `GET /tasks`, `/status`, `/inbox` and `/habits`,
- `POST /capture`,
- `POST /task/{id}/done|snooze|dismiss`,
- `POST /task/{id}` (edit time). An edit on a recurring task changes every later occurrence, as in the TUI,
- the inbox schedule and delete routes from the TUI design.

It sends `"source": "phone"`.

### Alarm path

1. The Mac app subscribes to the ntfy JSON stream on the Pi, like `desk/ntfy.go`.
2. For each alarm message, it gets the task ID and sends a Web Push to the stored subscription.
   - Push payload: `{title, body, taskId, tag}`.
   - `tag` is the task ID, so a repeat alarm replaces the old notification.
3. The service worker shows the notification with actions `done` and `snooze`.
4. On an action, the service worker sends `POST /api/task/{id}/{action}` to the Mac app, then closes the notification.
5. A tap on the notification body opens the app on Today.

Repeat alarms escalate (user's decision). The daemon republishes every 60 s and raises the ntfy priority 3 → 4 → 5 at 3 and 6 minutes.
- The push payload carries the ntfy priority.
- The service worker sets `renotify: true` only when the priority rose since the last notification for that tag. Other repeats replace the notification without sound.
- Result: the phone buzzes at 0, 3 and 6 minutes, and stays quiet between.

Closing the phone notification when the alarm is acted on elsewhere (desk, TUI, Claude) is a nice-to-have, not part of this build (user's decision).

The stream reconnects with backoff. A dropped stream must not lose an alarm silently.
- **Builder's call:** how. One option: on reconnect, check `GET /status` for `alarm_firing` and push if the alarm was not pushed yet.

### Screens

- **Today:** grouped Overdue, Next, Later, Done (collapsed). Tap a row for actions: done, snooze, edit time.
- **Habits:** streak and a 4-week grid per habit, rendered from the daemon's `GET /habits`. No habit logic in the PWA.
- **Inbox:** list. Tap to schedule with free text. Swipe or long-press to delete.
- **Quick-add:** a text field always at the bottom. Sends free text to `/capture` and shows the daemon's echo.
- Offline or daemon down: show the last data with a visible "stale since HH:MM" banner, and disable actions.

### Android PWA details

- `manifest.webmanifest`: `display: standalone`, 192 and 512 icons plus a maskable icon.
- A manifest shortcut "Add task" that opens straight to quick-add.
- An in-app button to turn on notifications. It asks for permission and then subscribes.
- `POST /api/push/test` to send a test notification.
- The service worker caches the app shell only, never `/api/*`.

### Security

- The same guard as desk and Body Ledger: allowed hosts (421), allowed `Tailscale-User-Login` (403), same-origin JSON on unsafe methods, strict CSP. Only `/api/health` is open.
- VAPID keys and the subscription are in a Docker volume, in files with mode 0600.
- The container runs as non-root.

## Constraints

- Android Chrome only.
- Config through `.env`, from `.env.example`: Pi daemon URL, ntfy URL and topic, allowed hosts and users, VAPID subject.
- A `/api/health` check.
- No change to the ntfy server or desk.

## Left to the builder

- Visual design, within the TUI's spirit: dark, calm, state-grouped.
- Push retry policy, beyond deleting the subscription on 404/410.
- Polling interval for the screens while the app is open.
