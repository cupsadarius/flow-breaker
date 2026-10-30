# Decisions

## 2026-10-30 — The daemon computes habits and streaks (`GET /habits`)
Why: the TUI and the phone PWA must show the same streaks; one implementation cannot disagree with itself.
Rejected: habit logic in each client — two copies of one rule.

## 2026-10-30 — A dismissed slot counts as missed in habits
Why: keeps streaks honest; `Task.History` records only done days, so no schema change.
Rejected: dismissed as done or neutral — needs a three-state history and a `resetDaily` change.

## 2026-10-30 — Mac calendar sync starts lazily with `open -g -j`, not launchd
Why: SentinelOne killed launchd processes that send data out; `open` on a running app is a no-op, so single-instance is free.
Rejected: LaunchAgent with KeepAlive — killed by SentinelOne before.
