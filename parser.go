package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── Capture text parser ──────────────────────────────────────────────────────
//
// Turns free-capture text into a structured 5-field result. The pipeline is:
//
//	heuristics (unambiguous only) → miss?
//	  → no time signal:  inbox floor (never touch the network)
//	  → time signal:     one Haiku call
//	      → success:     scheduled
//	      → error/timeout/empty: inbox floor
//
// Fail loud, never silently drop: anything the heuristics can't confidently
// place, and anything Haiku can't resolve, lands in the inbox rather than being
// scheduled at a guessed time.

// ParseResult is the 5-field contract emitted identically by heuristics and
// Haiku. Days uses the store's weekdayShort form (e.g. "mon", "thu").
type ParseResult struct {
	Time       string     // "HH:MM" (24h); "" if none
	Date       string     // "YYYY-MM-DD"; today if unresolved
	Recurrence Recurrence // once/daily/weekdays/weekly/monthly (existing enum)
	Days       []string   // weekday set for weekly; store's weekdayShort form
	Desc       string     // the stripped remainder
}

var reHHMM = regexp.MustCompile(`^\d{2}:\d{2}$`)

// valid reports whether the result is safely schedulable — the trust boundary
// for Haiku output (and cheap insurance for heuristics). A result is schedulable
// only if it can actually be placed on the clock, so a non-empty valid Time is
// REQUIRED: every recurrence here (once/daily/weekdays/weekly/monthly) fires at
// a time-of-day, and empty()/all-fields-empty can never be true for real schema
// output (recurrence is a required enum). Also validates Date, the recurrence
// enum, and that every Days entry is a store weekdayShort code ("mon", not
// "monday"). Anything failing → inbox floor.
func (r ParseResult) valid() bool {
	if !reHHMM.MatchString(r.Time) {
		return false
	}
	h, _ := strconv.Atoi(r.Time[:2])
	m, _ := strconv.Atoi(r.Time[3:])
	if h > 23 || m > 59 {
		return false
	}
	if _, err := time.Parse("2006-01-02", r.Date); err != nil {
		return false
	}
	switch r.Recurrence {
	case Once, Daily, Weekdays, Weekly, Monthly:
	default:
		return false
	}
	for _, d := range r.Days {
		if !isWeekdayShort(d) {
			return false
		}
	}
	return true
}

// isWeekdayShort reports whether d is one of the store's 3-letter weekday codes.
func isWeekdayShort(d string) bool {
	for _, w := range weekdayShort {
		if w == d {
			return true
		}
	}
	return false
}

// Anchored, case-insensitive heuristic patterns. Each captures the numeric parts
// and the full span to strip. Order of evaluation matters (see parseHeuristics):
// recurrence and "tomorrow" are checked before the bare at/la forms, since e.g.
// "every weekday at 9" contains "at 9".
var (
	// relative: in N min(utes)  /  peste N (de) minute
	reInMinutes = regexp.MustCompile(`(?i)\bin\s+(\d+)\s*min(?:ute)?s?\b`)
	reInHours   = regexp.MustCompile(`(?i)\bin\s+(\d+)\s*h(?:ours?|rs?)?\b`)
	rePesteMin  = regexp.MustCompile(`(?i)\bpeste\s+(\d+)\s*(?:de\s+)?minute?\b`)
	rePesteHour = regexp.MustCompile(`(?i)\bpeste\s+(\d+)\s*(?:h\b|ore\b|oră\b|ora\b)`)

	// recurring
	reEveryWeekday = regexp.MustCompile(`(?i)\bevery\s+weekdays?\s+at\s+(\d{1,2})(?::(\d{2}))?\b`)
	// EN daily: "every day/night/evening/morning [at H[:MM]]". Checked before
	// reAt, which would otherwise match the "at H" tail and fabricate a Once.
	// "every weekday" cannot match here — \bday needs a boundary before "day",
	// and "weekday" has none.
	reEveryDay = regexp.MustCompile(`(?i)\bevery\s+(?:day|night|evening|morning)\b(?:\s+at\s+(\d{1,2})(?::(\d{2}))?)?`)
	// RE2's \b is ASCII-only and won't match before "î" (U+00EE), so instead
	// anchor on start-of-string or whitespace. Without this, "in"/"în" inside
	// "din fiecare" / "puțin fiecare" would match and fabricate a daily task.
	reInFiecareZi = regexp.MustCompile(`(?i)(?:^|\s)[iî]n\s+fiecare\s+zi(?:\s+la\s+(\d{1,2})(?::(\d{2}))?)?\b`)

	// tomorrow H[:MM]  /  mâine|maine H[:MM]
	reTomorrow = regexp.MustCompile(`(?i)\btomorrow\s+(\d{1,2})(?::(\d{2}))?\b`)
	reMaine    = regexp.MustCompile(`(?i)\bm[aâ]ine\s+(\d{1,2})(?::(\d{2}))?\b`)

	// at H[:MM]  /  la H[:MM]
	reAt = regexp.MustCompile(`(?i)\bat\s+(\d{1,2})(?::(\d{2}))?\b`)
	reLa = regexp.MustCompile(`(?i)\bla\s+(\d{1,2})(?::(\d{2}))?\b`)
)

// resolveClock validates an explicit H[:MM] against the 24h clock and the
// AM/PM-ambiguity gate. A bare hour (no minutes) in 1..6 is genuinely ambiguous
// (3 could be 3am or 3pm), so it is rejected here and left for Haiku — a wrong
// heuristic match is worse than a miss. With minutes present the value is taken
// literally as 24h.
func resolveClock(hourStr, minStr string) (h, m int, ok bool) {
	h, err := strconv.Atoi(hourStr)
	if err != nil || h > 23 {
		return 0, 0, false
	}
	hasMin := minStr != ""
	if hasMin {
		m, err = strconv.Atoi(minStr)
		if err != nil || m > 59 {
			return 0, 0, false
		}
	}
	if !hasMin && h >= 1 && h <= 11 {
		// A bare hour 1..11 is AM/PM-ambiguous — a personal reminder "at 8"
		// almost always means 20:00, not 08:00 — so leave it for Haiku. Only
		// 0, 12, and 13..23 (unambiguously 24h) or any colon form resolve here.
		return 0, 0, false
	}
	return h, m, true
}

// strip removes the matched span from text and returns the trimmed remainder as
// the description.
func strip(text string, loc []int) string {
	remainder := text[:loc[0]] + text[loc[1]:]
	return strings.TrimSpace(collapseSpaces(remainder))
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// parseHeuristics matches ONLY unambiguous time expressions, using an injected
// clock (never time.Now()). On a match it strips the matched span and returns
// the remainder as Desc with ok=true; otherwise ok=false and the caller decides
// between the inbox floor and a Haiku call.
func parseHeuristics(text string, now time.Time) (ParseResult, bool) {
	// ── relative offsets (Once; roll date across midnight) ───────────────
	if loc := reInMinutes.FindStringSubmatchIndex(text); loc != nil {
		return relativeResult(text, loc, now, time.Duration(atoi(text, loc, 2))*time.Minute), true
	}
	if loc := rePesteMin.FindStringSubmatchIndex(text); loc != nil {
		return relativeResult(text, loc, now, time.Duration(atoi(text, loc, 2))*time.Minute), true
	}
	if loc := reInHours.FindStringSubmatchIndex(text); loc != nil {
		return relativeResult(text, loc, now, time.Duration(atoi(text, loc, 2))*time.Hour), true
	}
	if loc := rePesteHour.FindStringSubmatchIndex(text); loc != nil {
		return relativeResult(text, loc, now, time.Duration(atoi(text, loc, 2))*time.Hour), true
	}

	// ── recurring (checked before bare at/la, which they contain) ─────────
	if loc := reEveryWeekday.FindStringSubmatchIndex(text); loc != nil {
		// A matched-but-invalid recurring clock must floor, not fall through to a
		// later bare at/la matcher (which would fabricate a Once from the same
		// string). Symmetric with the în-fiecare-zi branch below.
		h, m, ok := resolveClock(group(text, loc, 2), group(text, loc, 4))
		if !ok {
			return ParseResult{}, false
		}
		// Recurring: leave Date empty so it fires every applicable weekday. A
		// Date here would pin it to its creation day (see shouldFireTodayAt).
		return ParseResult{
			Time:       fmt.Sprintf("%02d:%02d", h, m),
			Recurrence: Weekdays,
			Desc:       strip(text, loc[0:2]),
		}, true
	}
	if loc := reEveryDay.FindStringSubmatchIndex(text); loc != nil {
		timeStr := ""
		if group(text, loc, 2) != "" {
			h, m, ok := resolveClock(group(text, loc, 2), group(text, loc, 4))
			if !ok {
				return ParseResult{}, false
			}
			timeStr = fmt.Sprintf("%02d:%02d", h, m)
		}
		return ParseResult{
			Time:       timeStr,
			Recurrence: Daily,
			Desc:       strip(text, loc[0:2]),
		}, true
	}
	if loc := reInFiecareZi.FindStringSubmatchIndex(text); loc != nil {
		timeStr := ""
		if group(text, loc, 2) != "" {
			h, m, ok := resolveClock(group(text, loc, 2), group(text, loc, 4))
			if !ok {
				return ParseResult{}, false
			}
			timeStr = fmt.Sprintf("%02d:%02d", h, m)
		}
		// Recurring: leave Date empty so it fires every day (see the Weekdays
		// branch above and shouldFireTodayAt's Once-scoped gate).
		return ParseResult{
			Time:       timeStr,
			Recurrence: Daily,
			Desc:       strip(text, loc[0:2]),
		}, true
	}

	// ── tomorrow / mâine (Once, next day) ─────────────────────────────────
	if r, ok := clockResult(text, reTomorrow, now.AddDate(0, 0, 1)); ok {
		return r, true
	}
	if r, ok := clockResult(text, reMaine, now.AddDate(0, 0, 1)); ok {
		return r, true
	}

	// ── at / la (Once, today) ─────────────────────────────────────────────
	if r, ok := clockResult(text, reAt, now); ok {
		return r, true
	}
	if r, ok := clockResult(text, reLa, now); ok {
		return r, true
	}

	return ParseResult{}, false
}

// relativeResult builds a Once result at now+offset, rolling the date to the
// next day when the offset crosses midnight.
func relativeResult(text string, loc []int, now time.Time, offset time.Duration) ParseResult {
	target := now.Add(offset)
	return ParseResult{
		Time:       target.Format("15:04"),
		Date:       target.Format("2006-01-02"),
		Recurrence: Once,
		Desc:       strip(text, loc[0:2]),
	}
}

// clockResult applies an at/la/tomorrow-style regex (hour in group 1, optional
// minute in group 2), resolving against `day` for the date. Returns ok=false if
// the pattern doesn't match or the hour fails the ambiguity gate.
func clockResult(text string, re *regexp.Regexp, day time.Time) (ParseResult, bool) {
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return ParseResult{}, false
	}
	h, m, ok := resolveClock(group(text, loc, 2), group(text, loc, 4))
	if !ok {
		return ParseResult{}, false
	}
	return ParseResult{
		Time:       fmt.Sprintf("%02d:%02d", h, m),
		Date:       day.Format("2006-01-02"),
		Recurrence: Once,
		Desc:       strip(text, loc[0:2]),
	}, true
}

// group returns submatch n (0-indexed pair in loc), or "" if that group did not
// participate.
func group(text string, loc []int, n int) string {
	if n+1 >= len(loc) || loc[n] < 0 {
		return ""
	}
	return text[loc[n]:loc[n+1]]
}

// atoi parses submatch n as an int (0 on failure — callers only pass groups
// that matched \d+).
func atoi(text string, loc []int, n int) int {
	v, _ := strconv.Atoi(group(text, loc, n))
	return v
}

// timeSignalKeywords are the EN+RO cues that route a heuristic miss to Haiku
// rather than straight to the inbox. Matched case-insensitively on word-ish
// boundaries. "în fiecare" is covered by "în"/"in"? No — checked as a phrase.
var reTimeSignal = regexp.MustCompile(`(?i)(\d|\bat\b|\bin\b|\btomorrow\b|\bevery\b|\bla\b|\bpeste\b|\bm[aâ]ine\b|[iî]n\s+fiecare)`)

// hasTimeSignal reports whether the text contains any digit or time keyword
// (EN+RO). A capture with no signal never reaches Haiku — it goes straight to
// the inbox.
func hasTimeSignal(text string) bool {
	return reTimeSignal.MatchString(text)
}

// HaikuFn resolves a heuristic miss via one model call. now is threaded through
// (not read from the wall clock) so heuristics and Haiku agree on "today" even
// across a midnight boundary. Tests inject a fake.
type HaikuFn func(ctx context.Context, text string, now time.Time) (ParseResult, error)

// Parse is the dispatcher. haiku lets tests inject a fake; production wires
// parseHaiku with a 3s timeout (see DefaultParse). It returns the parsed result
// plus a destination ("scheduled"|"inbox") and provenance
// ("heuristics"|"haiku"|"none").
func Parse(text string, now time.Time, haiku HaikuFn) (res ParseResult, dest string, parsedBy string) {
	return parseWithTimeout(text, now, haiku, 3*time.Second)
}

// inboxFloor is the fallback result for anything not confidently schedulable:
// the raw capture kept as-is, dated today.
func inboxFloor(text string, now time.Time) (ParseResult, string, string) {
	return ParseResult{Date: now.Format("2006-01-02"), Desc: strings.TrimSpace(text)}, "inbox", "none"
}

// parseWithTimeout is Parse with an injectable Haiku timeout so the timeout path
// can be tested without a real 3s wait.
func parseWithTimeout(text string, now time.Time, haiku HaikuFn, timeout time.Duration) (ParseResult, string, string) {
	// valid() is cheap insurance on the heuristic leg too: a heuristic "hit" that
	// isn't actually schedulable (e.g. a bare "în fiecare zi" with no time) falls
	// through rather than being scheduled with no clock.
	if res, ok := parseHeuristics(text, now); ok {
		if res.Date == "" { // recurring heuristics leave Date unresolved
			res.Date = now.Format("2006-01-02")
		}
		if res.valid() {
			return res, "scheduled", "heuristics"
		}
	}
	if !hasTimeSignal(text) {
		return inboxFloor(text, now)
	}
	// Time-ish signal present but heuristics missed → one Haiku call, guarded by
	// a timeout. Any failure — error, timeout, or a result that fails validation
	// at the trust boundary — falls to the inbox floor. Never a silently dropped
	// or guessed task.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := haiku(ctx, text, now)
	if err != nil || !res.valid() {
		return inboxFloor(text, now)
	}
	return res, "scheduled", "haiku"
}

// DefaultParse wires the real parseHaiku with a 3s timeout. Production entry
// point; tests use Parse/parseWithTimeout with a fake.
func DefaultParse(text string, now time.Time) (ParseResult, string, string) {
	return Parse(text, now, parseHaiku)
}
