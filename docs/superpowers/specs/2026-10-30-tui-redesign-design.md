# TUI redesign: Today | Habits (variant A)

## Intent

The current TUI is one flat table plus a long key strip. It looks bad and is not intuitive.
The user wants flow-breaker to feel like a **task manager and habit builder**.
Variant A (two panes) was picked from three mockups in a brainstorm on 2026-10-30.

## Goals

- Show today as an agenda grouped by state, so the one thing that matters stands out.
- Make habits first-class: streak and a 4-week grid per habit.
- Show the inbox. The daemon stores it (`Store.Inbox`), but no client shows it today.
- Make adding a task one line of free text, through the existing capture parser (`POST /capture`).
- Show only the keys that matter on the current screen. `?` shows the rest.

## Non-goals

- Settings editing. It stays read-only, as today.
- Changes to scheduling, alarms or the escalation ladder.
- Calendar sync. See `2026-10-30-mac-calendar-sync-design.md`.

## Anti-goals

- A flat table with many columns again.
- Many single-letter modes that the user must remember.
- A false "all clear" when the daemon is offline. The current fail-loud offline behavior stays.

## Layout

```
flow-breaker  Fri 30 Oct · 13:30                                    ● daemon
─ TODAY ──────────────────── 3/5 ─┬─ HABITS ───────── streak ─ 4 weeks ──
 OVERDUE                          │  push-ups    🔥 12  ▪▪▪▪▪·· ▪▪▪▪▪·· …
 ! 13:00  stretch break  30m ago  │  squats      🔥  4  ▪▪▪▪▪·· ▪▪▪▪▪·· …
 NEXT                             │  this week  17/20 · 85%
▸ 15:00  squats          in 1h30  │─ INBOX ────────────────────────── 2 ─
   #exercise · weekdays           │  📥 call plumber
 LATER                            │  📥 renew passport sometime
   16:30  push-ups + Garmin       │
 DONE (2)  ▸ expand               │
──────────────────────────────────┴────────────────────────────────────
 + add: "standup 9:30 weekdays #work" …
 ⏎ done  s snooze  e edit  tab habits  / add  ? more
```

The browser mockup is in `.superpowers/brainstorm/*/content/variants.html` (option A).

### Header

One line: name, date and time, and a daemon dot.
- Green: synced.
- Red: offline. The current offline banner text ("daemon unreachable — stale as of HH:MM") stays below the header.
- When the alarm fires, the current full-width flashing alarm bar shows above the panes. Alarm keys stay the same: `space`/`enter` dismiss, `s` snooze, `c` done.

### Today pane (left, about 45% width)

- Shows tasks that fire today (`shouldFireToday`), grouped as Overdue, Next, Later and Done.
  - Overdue: not done or dismissed, and the due time has passed.
  - Next: the first upcoming task.
  - Later: the other upcoming tasks.
  - Done: done or dismissed. Collapsed by default. `enter` on the "DONE (n)" row expands it.
- Empty groups do not show.
- The pane title shows done/total for today.
- The selected row shows one more line: tags and repeat rule.
- Tasks that do not fire today do not show here.

### Habits pane (right, top)

- One row per habit. A habit is the set of recurring tasks (not `Once`) that share one description.
  - **Builder's call:** the grouping key. The exact `Desc` is the default. Today's data has "Set: push-ups (exercise)" and "Set: push-ups + save Garmin activity (exercise)", which then count as two habits. Pick a better key only if it stays simple.
- Columns: name, current streak, 4-week grid (Mon–Sun weeks, oldest left), from `Task.History`.
- Grid cells: done (green), missed (red), not scheduled or future (dim dot).
- A dismissed slot counts as missed, in the grid and the streak (user's decision). No store schema change.
- Streak: consecutive scheduled days, counting back from today, on which each slot of the habit was done. A slot not yet due today does not break the streak.
- A footer line shows "this week: done/scheduled · %".
- **The daemon computes habits** (Claude's call), so the TUI and the phone PWA cannot disagree: `GET /habits` returns, per habit, the name, task IDs, slot times, current streak, 28 grid cells and week totals. Pure Go functions with tests; the TUI only renders.
- The pane is read-only. `enter` on a habit moves focus to that habit's next open slot in Today (Claude's call).

### Inbox pane (right, bottom)

- Lists `Store.Inbox` items.
- `enter` on an item opens a one-line prompt for a time and repeat rule (free text, same parser). The item then moves from the inbox to the tasks. The parser reads time and recurrence from the prompt text only; the task keeps the inbox item's description (Claude's call).
- `d` deletes an inbox item, after a y/n prompt.

### Quick-add bar

- `/` (and `a`) focuses the bar at the bottom. The bar takes free text and sends it to `POST /capture`.
- The result echo from the daemon shows as the transient message, for example "📥 inbox — …" or the scheduled time.
- `#word` tokens become tags: `handleCapture` strips them from the text before parsing and stores them as tags (Claude's call; today they stay in the description). The placeholder example must use a phrase the parser already handles, for example "standup every weekday at 9:30 #work".
- `ctrl+e` in the bar opens the full form for the cases the parser cannot express (custom days, tags). **Builder's call:** keep the current form flow or make it one screen with all fields visible. One screen is preferred.

### Keys

- `tab` / `shift+tab`: move focus between Today, Habits and Inbox.
- `j`/`k`, `g`/`G`: move in the focused pane.
- `enter`: done (Today), schedule (Inbox), expand (DONE row).
- `s` snooze, `e` edit, `d` delete with confirm, `/` add.
- `?`: an overlay that lists all keys and opens Archive, Calendar feeds and Settings. These screens become overlays over the panes. They do not get their own keys on the main screen.
- Removed (user's decision): `p` calendar suggest, timeline and manual import (calendar events materialize automatically); `r` manual refresh (the TUI syncs every second); `h`/`f` full-history habit view (the 4-week grid replaces it). Remove their code too.
- `q` quit.
- The footer shows at most 6 keys, for the focused pane.

### Narrow terminals

Below 100 columns, the panes stack: Today on top, then Habits, then Inbox. `tab` still moves focus.

## Daemon changes

The daemon has no inbox API today. Add:
- `GET /inbox`: list inbox items.
- `POST /inbox/{id}/schedule` with `{"text": "..."}`: parse the text with the capture parser. On success, move the item to the tasks. On a parse failure, return 422 and leave the item in the inbox.
- `DELETE /inbox/{id}`.
- `GET /habits` (see Habits pane).
- `#tag` extraction in `handleCapture` (see Quick-add bar).

The TUI sync (`syncCmd`, every second) also gets `/inbox` and `/habits`.

## Constraints

- Go, Bubble Tea, Lip Gloss. No new dependency.
- The TUI stays a client of the daemon. It never writes the store.
- The current offline behavior stays: keep the last cache, fail loud, block mutations.

## Left to the builder

- File layout. `tui.go` is 2083 lines; splitting it is allowed.
- Exact colors. Keep the 16-color ANSI palette so the theme follows the terminal.
- Scroll behavior inside each pane.
