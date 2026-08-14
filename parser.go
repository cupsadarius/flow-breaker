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

// empty reports whether the result carries nothing schedulable — no time, no
// recurrence, no explicit weekday set. Used to apply the inbox floor when Haiku
// returns a result that can't actually be placed on the clock.
func (r ParseResult) empty() bool {
	return r.Time == "" && r.Recurrence == "" && len(r.Days) == 0
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
	// No leading \b: RE2's \b is ASCII-only and won't match before "î" (U+00EE).
	// The multi-word phrase is distinctive enough that a bare start is safe.
	reInFiecareZi = regexp.MustCompile(`(?i)[iî]n\s+fiecare\s+zi(?:\s+la\s+(\d{1,2})(?::(\d{2}))?)?\b`)

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
	if !hasMin && h >= 1 && h <= 6 {
		return 0, 0, false // bare AM/PM-ambiguous hour → leave for Haiku
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
	today := now.Format("2006-01-02")

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
		if h, m, ok := resolveClock(group(text, loc, 2), group(text, loc, 4)); ok {
			return ParseResult{
				Time:       fmt.Sprintf("%02d:%02d", h, m),
				Date:       today,
				Recurrence: Weekdays,
				Desc:       strip(text, loc[0:2]),
			}, true
		}
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
		return ParseResult{
			Time:       timeStr,
			Date:       today,
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

// Parse is the dispatcher. haiku lets tests inject a fake; production wires
// parseHaiku with a 3s timeout (see DefaultParse). It returns the parsed result
// plus a destination ("scheduled"|"inbox") and provenance
// ("heuristics"|"haiku"|"none").
func Parse(text string, now time.Time, haiku func(context.Context, string) (ParseResult, error)) (res ParseResult, dest string, parsedBy string) {
	return parseWithTimeout(text, now, haiku, 3*time.Second)
}

// parseWithTimeout is Parse with an injectable Haiku timeout so the timeout path
// can be tested without a real 3s wait.
func parseWithTimeout(text string, now time.Time, haiku func(context.Context, string) (ParseResult, error), timeout time.Duration) (ParseResult, string, string) {
	if res, ok := parseHeuristics(text, now); ok {
		return res, "scheduled", "heuristics"
	}
	if !hasTimeSignal(text) {
		return ParseResult{Date: now.Format("2006-01-02"), Desc: strings.TrimSpace(text)}, "inbox", "none"
	}
	// Time-ish signal present but heuristics missed → one Haiku call, guarded by
	// a timeout. Any failure (error, timeout, or an empty/unschedulable result)
	// falls through to the inbox floor — never a silently dropped or guessed task.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := haiku(ctx, text)
	if err != nil || res.empty() {
		return ParseResult{Date: now.Format("2006-01-02"), Desc: strings.TrimSpace(text)}, "inbox", "none"
	}
	return res, "scheduled", "haiku"
}

// DefaultParse wires the real parseHaiku with a 3s timeout. Production entry
// point; tests use Parse/parseWithTimeout with a fake.
func DefaultParse(text string, now time.Time) (ParseResult, string, string) {
	return Parse(text, now, parseHaiku)
}
