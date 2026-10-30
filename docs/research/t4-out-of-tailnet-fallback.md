# T4 — Out-of-Tailnet Fallback Alert Channel

**Ticket:** cupsadarius/flow-breaker#5 (wayfinder T4)
**Date:** 2026-08-12
**Context:** Personal task-alarm daemon on a headless Raspberry Pi (Go, on a Tailscale tailnet).
Normal alert path is self-hosted **ntfy over the tailnet**. When the phone drops off the
tailnet (Tailscale toggled off, captive Wi-Fi), ntfy pushes fail silently. The Pi tracks
unacked alarms; if one goes ~15 min with zero interaction, it must fire over a channel that
does **not** depend on the tailnet.

---

## TL;DR recommendation

**Use the Telegram Bot API as the out-of-tailnet fallback.** Free, one plain HTTPS POST from
Go, no verified-sender / regulatory friction, and it reaches the phone over any public
internet path (mobile data or captive Wi-Fi) — i.e. exactly the failure mode that knocks out
the tailnet. Keep **SMS (Twilio, or a cheaper EU-friendly provider) as an optional deeper
tier** only if you want to cover the case where the phone has cellular *signal* but no
*data* at all. For flow-breaker's stated failure mode (phone off the tailnet but still online),
Telegram fully covers it at zero cost and lowest code.

---

## Option A — Telegram Bot API

### Setup on a headless Pi
- Create a bot by messaging [@BotFather](https://t.me/BotFather) once (`/newbot`) from any
  Telegram client. You get a **bot token** like `123456:ABC-DEF...`.
- Store the token + your personal `chat_id` in the Pi's config/env. **No interactive login on
  the Pi** — it only ever makes outbound HTTPS calls with the token. Perfect for headless.
- Getting your `chat_id`: message the bot once, then read
  `https://api.telegram.org/bot<token>/getUpdates` and pull `message.chat.id`. One-time.

### Cost
- **Free.** No per-message charge, no number leasing.
  ([Telegram Bot API](https://core.telegram.org/bots/api))

### Reliability as an escape hatch
- Does **not** route through the tailnet. It needs the *phone* to have any internet (mobile
  data or Wi-Fi) and the *Pi* to have outbound HTTPS to `api.telegram.org`.
- This matches the target failure modes: Tailscale toggled off but data is up → Telegram
  works; captive Wi-Fi that has completed its portal → Telegram works.
- **Gap:** if the phone has *no data at all* (dead data plan, deep signal loss with only
  voice/SMS), Telegram cannot reach it. That's the one scenario where only SMS wins.

### Triggering from Go
Single HTTPS POST, no SDK:

```
POST https://api.telegram.org/bot<token>/sendMessage
Content-Type: application/json
{"chat_id": <id>, "text": "ALARM: <task> unacked 15m"}
```

`GET` also works with query params. Method + endpoint format confirmed in the
[sendMessage docs](https://core.telegram.org/bots/api#sendmessage). In Go this is
`http.Post` with a JSON body — no dependency beyond the stdlib.

### Gotchas
- **The user must message the bot first.** Per Telegram's own docs: *"Bots can't start
  conversations with users. A user must either add them to a group or send them a message
  first."* ([core.telegram.org/bots](https://core.telegram.org/bots)). If you skip this the
  API returns `403 Forbidden: bot can't initiate conversation with a user`. For a personal
  tool this is a one-time `/start` and never recurs.
- Bot tokens are bearer credentials — treat like a secret (env/file perms on the Pi).
- Telegram must not be blocked on whatever network the phone is on (rare for personal use,
  but note it for hostile captive portals / some corporate Wi-Fi).

---

## Option B — SMS gateway

### Setup on a headless Pi
- Sign up with a provider, get **API credentials** (Account SID + Auth Token for Twilio, or
  an API key). Purchase/lease a sender (a number, or a registered alphanumeric sender ID).
- All headless-friendly: outbound REST calls with credentials in env. No interactive login.

### Cost (to a Romanian +40 mobile)
Per-message outbound SMS, from provider/aggregator data:

| Provider | Price/SMS to RO (+40) | Notes |
|---|---|---|
| **Twilio** | **~$0.078** (page shows $0.0781; aggregators ~$0.0737) | [Twilio RO SMS pricing](https://www.twilio.com/en-us/sms/pricing/ro) |
| Infobip | ~$0.060 | aggregator data |
| Sinch | ~$0.061 | aggregator data |
| Plivo | ~$0.061 | aggregator data |
| MessageBird (Bird) | ~$0.025 | aggregator data, EU-competitive |
| Telnyx | ~$0.004 (advertised) | suspiciously low; verify on Telnyx's own pricing before trusting |

Sources: [Twilio RO pricing](https://www.twilio.com/en-us/sms/pricing/ro),
[Sent — Romania SMS pricing 2025](https://www.sent.dm/en/resources/sms-pricing/romania-sms-pricing).
Twilio also leases an international number from ~$1.15/mo and charges a $0.001 failed-message
processing fee. For an alarm daemon that fires rarely, monthly cost is dominated by the
number lease / any sender-ID registration, not per-message volume.

### Reliability as an escape hatch
- **Strongest genuine escape hatch:** SMS reaches the phone over the cellular network with no
  data connection at all — the only option that survives "phone has signal but zero internet."
- The Pi still needs outbound internet to hit the provider's API, so it is not fully
  independent of infrastructure, but it is fully independent of the *phone's* data path.

### Triggering from Go
- Twilio: REST `POST` to `/2010-04-01/Accounts/<SID>/Messages.json` with basic-auth
  (SID\:token) and form fields `To/From/Body`. Official Go helper library exists, but a raw
  `http.Post` works too.
- Other providers: similar REST + API key. Slightly more moving parts than Telegram (auth
  scheme, sender field, delivery-status handling).

### Gotchas (Romania-specific, and they are real)
- **Sender ID / registration friction.** Romania requires alphanumeric sender-ID
  registration: *mandatory* for Telekom Romania (MNC 03), optional-but-recommended for
  Orange/Vodafone/Digi, with ~4-week provisioning and a **one-time ~$760** cross-network
  registration fee on Twilio. Numeric sender IDs are no longer supported on Telekom RO and
  get overwritten on other networks.
  ([Twilio RO SMS guidelines](https://www.twilio.com/en-us/guidelines/ro/sms),
  [Twilio support — RO sender ID registration](https://support.twilio.com/hc/en-us/articles/7409840359067)).
- **URL blocking.** Romanian operators filter/block SMS containing URLs unless the URL is
  allowlisted during registration — so don't put an ntfy/dashboard link in the alarm text.
- **GDPR / consent rules** apply to marketing SMS (STOP/DEZABONARE opt-out, etc.). Largely
  irrelevant for A2P alerts to your own number, but it's the regulatory backdrop.

---

## Head-to-head

| | Telegram Bot API | SMS (Twilio / EU provider) |
|---|---|---|
| Cost | Free | ~$0.025–0.078/msg + number lease + up to ~$760 RO sender-ID reg |
| Setup on headless Pi | Token + chat_id, no login | API creds + sender, no login |
| Independent of tailnet | Yes | Yes |
| Reaches phone with **no data at all** | **No** | **Yes** |
| Trigger from Go | 1 stdlib HTTPS POST | REST + auth (SDK optional) |
| Main gotcha | User must `/start` bot once | RO sender-ID registration, URL blocking, cost |
| Time to working | Minutes | Days (registration) + ongoing cost |

## Decision

Adopt **Telegram** as the out-of-tailnet fallback. It defeats the actual failure mode
(tailnet down, internet up) for free, with a single stdlib HTTPS POST and no regulatory
overhead. The only scenario it misses is "phone has cellular signal but no data," which is a
narrow edge for a personal device. If that edge matters to you later, add **SMS as a second,
deeper tier** — but treat the RO alphanumeric sender-ID registration (~4 weeks, ~$760 for full
network coverage) and URL-blocking rules as the real cost of that tier, not the per-message
price. Practical escalation ladder: **ntfy (tailnet) → Telegram (any internet) → SMS (any
cellular signal)**, adding tiers only as needed.

---

### Sources
- [Telegram Bot API — sendMessage](https://core.telegram.org/bots/api#sendmessage)
- [Telegram — Bots: an introduction for developers](https://core.telegram.org/bots) (bots can't initiate conversations)
- [Twilio — SMS pricing, Romania](https://www.twilio.com/en-us/sms/pricing/ro)
- [Twilio — Romania SMS guidelines](https://www.twilio.com/en-us/guidelines/ro/sms)
- [Twilio support — Register alphanumeric sender ID in Romania](https://support.twilio.com/hc/en-us/articles/7409840359067)
- [Sent — Romania SMS pricing 2025 (provider comparison, ANCOM notes)](https://www.sent.dm/en/resources/sms-pricing/romania-sms-pricing)
