# T8 — Client setup checklist

Manual device config for v2 capture + notify clients. **No speech code** — both OSes hand the daemon plain text.

Execute this **after the daemon is deployed** (T3) and the capture endpoint is live (T7). Producing this list is planning; running it is a build-phase step. Nothing here is a code change.

## Prerequisites (must be true before starting)

- Daemon running on the Pi, reachable at `https://<machine>.<tailnet>.ts.net` via `tailscale serve --bg 7777` (real TLS, no self-signed warnings).
- `POST /capture` live (T7 contract below).
- ntfy server on the Pi, bound to `tailscale0` behind `tailscale serve`, topic `flow` publishable.
- Test phone + Mac both on the tailnet (Tailscale up).

## Reference: T7 capture contract

```
POST https://<machine>.<tailnet>.ts.net/capture
{"text": "wash the dishes in 16 min", "source": "phone"}   # source optional, defaults "api"

200 (always — inbox is a valid outcome)
{ "echo": "14:12 — wash the dishes",
  "destination": "scheduled" | "inbox",
  "parsedBy": "heuristic" | "haiku",
  "task": { "id": 42, ... } }
```

Client shows `echo` as a toast. **Any non-200 or network error → red toast + local-note fallback** (see Fail-loud below).

---

## 1. Android — capture (HTTP Shortcuts)

App: **HTTP Shortcuts** (F-Droid or Play, open source).

- [ ] Create shortcut → **POST** to `https://<machine>.<tailnet>.ts.net/capture`.
- [ ] Add a **text-input variable** `input` (prompt on launch).
- [ ] Body (JSON): `{"text": "{{input}}", "source": "phone"}`  <!-- HTTP Shortcuts var syntax is {{name}}; the doc's `$$$input` was shorthand — confirm the exact token in-app -->
- [ ] Header `Content-Type: application/json`.
- [ ] Response handling → **show response as toast**, template = the `echo` field → free echo-back.
- [ ] On the input dialog, tap the **mic key (Gboard dictation)** to speak.
- [ ] Give it its **own home-screen icon**.
- [ ] **Fail-loud**: on non-2xx / connection error, show a **red** toast AND run the dumb fallback (append raw text to a local note / share-to-Keep). A capture tool that silently drops thoughts is worse than none.

_Deferred: hands-free (Tasker + AutoVoice). Don't start there — tap-and-speak is one gesture and never mishears a wake word._

## 2. Mac — capture (Shortcuts.app)

- [ ] New shortcut: **Dictate Text** → **Get Contents of URL** (POST, JSON body `{"text": <Dictated Text>, "source": "mac"}`) → **Show Result** (the `echo`).
- [ ] Assign a **global hotkey** in the Shortcuts sidebar — no window to find, works mid-flow.
- [ ] **Fail-loud**: on error, red result + append raw text to an Apple Note as fallback.

---

## 3. Android — notify (ntfy app)

- [ ] Install **ntfy**; add server `https://<machine>.<tailnet>.ts.net`, subscribe topic **`flow`**.
- [ ] **Exempt ntfy from battery optimization** + enable **instant delivery**. Self-hosted ntfy holds its own connection (not FCM) — if Android kills it, alerts stop with no error anywhere.
- [ ] Priority mapping (set by the publisher, verify on device): **5** = real alarm (bypass DND, vibrate, repeat sound); **3** = 10-min heads-up.
- [ ] Action buttons deliver on the notification (built server-side, T9): **Done** → `POST /task/{id}/done`, **Snooze** → `POST /task/{id}/snooze`. Tapping the notification *is* the state change — confirm both buttons hit the daemon and mutate.
- [ ] `flow-triage` topic is later (mute triage without muting alarms) — not part of this pass.

## 4. Mac — notify (`ntfy subscribe --exec`)

- [ ] `ntfy subscribe https://<machine>.<tailnet>.ts.net/flow --exec /path/to/alert-handler.sh` (daemonize via launchd).
- [ ] `alert-handler.sh` pipes each message into the existing v1 `osascript` / `say` / `afplay` code — all six v1 alert types survive unmodified, just triggered by subscription instead of the local ticker.
- [ ] TUI is now a viewer; the Mac keeps its mouth.

---

## Fail-loud / off-tailnet fallback (capture)

T7 makes off-tailnet delivery the **client's** job via a fallback ladder:

1. **Pi `/capture`** (tailnet up) — the happy path above.
2. **Telegram** — _blocked_: needs daemon-side Telegram *ingest* (poll bot → feed `/capture`). T4 spec'd Telegram for alerts-**out** only; capture-**in** via Telegram is not yet built or decided. Track before wiring tier 2.
3. **Local note** — always-works floor: append raw text to Apple Notes / Keep so nothing is lost. This is the tier every client MUST have today.

Until tier 2 exists, clients go **Pi → local note** (skip Telegram).

## End-to-end smoke test (after config)

- [ ] Speak a timed capture on the phone → toast echoes the parse → task appears in `/status`.
- [ ] Speak a timeless capture → echo says `inbox` → item lands in `inbox[]`.
- [ ] Fire a test alarm → phone buzzes at priority 5, bypasses DND → tap **Done** → task marked done in `/status`.
- [ ] Mac: same alarm speaks via `say`.
- [ ] Turn Tailscale **off** on the phone → capture → red toast + text saved to local note (nothing lost).
