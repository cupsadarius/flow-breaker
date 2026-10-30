# flow-breaker-web

The phone client: an installable PWA for Android Chrome, served from the always-on Intel Mac.

- Today, Habits and Inbox from the Pi daemon, plus a quick-add bar (free text, same parser as `flow-breaker capture`).
- Alarm notifications with **Done** and **Snooze** buttons. The server follows the Pi's ntfy topic and sends each alarm as a Web Push. A repeat alarm buzzes again only when its priority rises (0, 3 and 6 minutes).
- The phone talks only to this server. The server calls the Pi. Access needs your Tailscale identity (same guard as desk).

## Setup (Intel Mac)

1. `cp .env.example .env` and fill it in.
2. `docker compose up -d --build`
3. `tailscale serve --bg --https=8091 http://127.0.0.1:8091`

## Setup (phone)

1. Open `https://<intel-mac>.<tailnet>.ts.net:8091` in Chrome and install the app (menu → Install app).
2. Open the app and tap 🔔. A test notification arrives.
3. Remove the ntfy app from the phone, or every alarm notifies twice.

Long-press the app icon for the **Add task** shortcut.

## Data

VAPID keys (`vapid.json`) and the one phone subscription (`push.json`) live in the `data` volume, mode 0600. Delete `vapid.json` to rotate the keys; the phone then needs 🔔 again.
