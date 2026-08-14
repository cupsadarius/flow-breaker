# T3 — Raspberry Pi setup runbook

One-time checklist to bring up the `flow-breaker` daemon on the Pi as a
`systemd --user` service, exposed on the tailnet. Run the numbered steps once on
a fresh Pi; after that, day-to-day deploys are just `make deploy` from the Mac
(step 5).

The daemon binds **loopback only** (`127.0.0.1:7777`); nothing is reachable off
the box until `tailscale serve` fronts it (step 6). The `EnvironmentFile`
(`~/.config/flow-breaker/env`) holds the Anthropic API key and is **uncommitted**
(secret, `chmod 600`) — it lives only on the Pi.

## On the Pi

1. **Set the timezone** (belt-and-suspenders with the unit's `TZ=`):

   ```sh
   sudo timedatectl set-timezone Europe/Bucharest
   ```

2. **Enable lingering** so the user service runs headless without an active
   login session:

   ```sh
   loginctl enable-linger $USER
   ```

3. **Create the secret env file** (uncommitted, `chmod 600`):

   ```sh
   mkdir -p ~/.config/flow-breaker && \
     printf 'ANTHROPIC_API_KEY=sk-...\nFLOW_BREAKER_CAL_EMAIL=you@example.com\n' > ~/.config/flow-breaker/env && \
     chmod 600 ~/.config/flow-breaker/env
   ```

   `FLOW_BREAKER_CAL_EMAIL` is your own calendar address. It self-scopes the
   calendar **declined** filter: an event is dropped only when the
   `PARTSTAT=DECLINED` is on *your* attendee line, so a colleague declining a
   meeting you still plan to attend never suppresses your alarm. **Leave it unset
   and declined-filtering is disabled entirely** — every timed event materializes
   (over-alarm rather than silently miss).

4. **Install and enable the unit** (the repo must be checked out on the Pi, or
   copy `deploy/flow-breakerd.service` over):

   ```sh
   mkdir -p ~/.config/systemd/user && \
     cp deploy/flow-breakerd.service ~/.config/systemd/user/ && \
     systemctl --user daemon-reload && \
     systemctl --user enable --now flow-breakerd
   ```

## From the Mac

5. **Deploy the binary** (arm64 cross-build → scp → restart the service):

   ```sh
   make deploy            # or: make deploy PI_HOST=user@host
   ```

## Expose on the tailnet (on the Pi)

6. **Front the loopback listener with `tailscale serve`** (real TLS, reachable
   at `https://<machine>.<tailnet>.ts.net`):

   ```sh
   tailscale serve --bg 7777
   ```

## Verify

7. **Health checks:**

   ```sh
   # On the Pi (loopback):
   curl -sf http://127.0.0.1:7777/health

   # From the Mac (tailnet):
   curl -sf https://<machine>.<tailnet>.ts.net/health
   ```

## Notes

- `~/.config/flow-breaker/env` is the secret `EnvironmentFile` and is **not**
  committed. Recreate it per-host (step 3).
- The daemon binds `127.0.0.1:7777` only; the tailnet reaches it exclusively
  through `tailscale serve`. Override the bind with `FLOW_BREAKER_ADDR` if
  needed.
- Useful service commands: `systemctl --user status flow-breakerd`,
  `journalctl --user -u flow-breakerd -f`.
