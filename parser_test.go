package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// parseNow is a fixed clock for parser tests: Monday 2026-01-05, 10:00 local.
// Every parse call takes an injected now — no test ever reads the wall clock.
var parseNow = time.Date(2026, 1, 5, 10, 0, 0, 0, time.Local)

func eqDays(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqResult(a, b ParseResult) bool {
	return a.Time == b.Time && a.Date == b.Date &&
		a.Recurrence == b.Recurrence && a.Desc == b.Desc && eqDays(a.Days, b.Days)
}

func TestParseHeuristics(t *testing.T) {
	today := "2026-01-05"
	tomorrow := "2026-01-06"

	cases := []struct {
		name string
		text string
		want ParseResult
		ok   bool
	}{
		// ── relative (EN) ──────────────────────────────────────────────
		{"in N min", "call plumber in 15 min", ParseResult{Time: "10:15", Date: today, Recurrence: Once, Desc: "call plumber"}, true},
		{"in N minutes", "stretch in 5 minutes", ParseResult{Time: "10:05", Date: today, Recurrence: Once, Desc: "stretch"}, true},
		{"in Nh", "standup in 2h", ParseResult{Time: "12:00", Date: today, Recurrence: Once, Desc: "standup"}, true},
		{"in N crosses midnight", "backup in 900 min", ParseResult{Time: "01:00", Date: tomorrow, Recurrence: Once, Desc: "backup"}, true},
		// ── relative (RO) ──────────────────────────────────────────────
		{"peste N minute", "sună instalatorul peste 15 minute", ParseResult{Time: "10:15", Date: today, Recurrence: Once, Desc: "sună instalatorul"}, true},
		{"peste N de minute", "pauză peste 90 de minute", ParseResult{Time: "11:30", Date: today, Recurrence: Once, Desc: "pauză"}, true},
		{"peste Nh", "pauză peste 2h", ParseResult{Time: "12:00", Date: today, Recurrence: Once, Desc: "pauză"}, true},
		// ── at/la HH:MM (colon form always resolves, even low hours) ────
		{"at HH:MM", "meeting at 14:30", ParseResult{Time: "14:30", Date: today, Recurrence: Once, Desc: "meeting"}, true},
		{"at low HH:MM colon", "meeting at 08:30", ParseResult{Time: "08:30", Date: today, Recurrence: Once, Desc: "meeting"}, true},
		{"la HH:MM", "ședință la 15:00", ParseResult{Time: "15:00", Date: today, Recurrence: Once, Desc: "ședință"}, true},
		{"la H unambiguous (>=12)", "prânz la 13", ParseResult{Time: "13:00", Date: today, Recurrence: Once, Desc: "prânz"}, true},
		{"at 12 noon resolves", "lunch at 12", ParseResult{Time: "12:00", Date: today, Recurrence: Once, Desc: "lunch"}, true},
		// ── tomorrow / mâine ───────────────────────────────────────────
		{"tomorrow H (>=12)", "gym tomorrow 18", ParseResult{Time: "18:00", Date: tomorrow, Recurrence: Once, Desc: "gym"}, true},
		{"mâine H:MM", "curs mâine 9:30", ParseResult{Time: "09:30", Date: tomorrow, Recurrence: Once, Desc: "curs"}, true},
		{"maine no diacritic", "curs maine 9:30", ParseResult{Time: "09:30", Date: tomorrow, Recurrence: Once, Desc: "curs"}, true},
		// ── recurring ──────────────────────────────────────────────────
		// Recurring results carry NO Date — an empty Date means "every applicable
		// recurrence day". A Date here would pin them to the creation day (C1).
		{"every weekday at HH:MM", "standup every weekday at 09:30", ParseResult{Time: "09:30", Date: "", Recurrence: Weekdays, Desc: "standup"}, true},
		{"în fiecare zi", "citește în fiecare zi la 20", ParseResult{Time: "20:00", Date: "", Recurrence: Daily, Desc: "citește"}, true},
		{"every night at H (24h)", "meditate every night at 22", ParseResult{Time: "22:00", Date: "", Recurrence: Daily, Desc: "meditate"}, true},
		{"every day at HH:MM", "vitamins every day at 08:00", ParseResult{Time: "08:00", Date: "", Recurrence: Daily, Desc: "vitamins"}, true},
		// ── misses: AM/PM-ambiguous bare hours (1..11) → Haiku ──────────
		{"bare 3 ambiguous", "call plumber at 3", ParseResult{}, false},
		{"bare 8 ambiguous", "dinner at 8", ParseResult{}, false},
		{"bare 11 ambiguous", "call at 11", ParseResult{}, false},
		{"tomorrow bare 7 ambiguous", "gym tomorrow 7", ParseResult{}, false},
		{"every weekday bare 9 ambiguous", "standup every weekday at 9", ParseResult{}, false},
		{"every night bare 8 ambiguous", "meditate every night at 8", ParseResult{}, false},
		// ── misses: invalid / out-of-range clock → Haiku or inbox ───────
		{"invalid clock 25:99", "call at 25:99", ParseResult{}, false},
		// ── misses: no time / over-match guards ─────────────────────────
		{"no time at all", "call the plumber", ParseResult{}, false},
		{"timeless with keyword", "call plumber at some point", ParseResult{}, false},
		{"din fiecare not daily", "citește din fiecare zi", ParseResult{}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseHeuristics(c.text, parseNow)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, c.ok, got)
			}
			if ok && !eqResult(got, c.want) {
				t.Fatalf("result = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParseResultValid(t *testing.T) {
	cases := []struct {
		name string
		r    ParseResult
		want bool
	}{
		{"ok once", ParseResult{Time: "09:00", Date: "2026-01-05", Recurrence: Once}, true},
		{"ok weekly with days", ParseResult{Time: "09:00", Date: "2026-01-05", Recurrence: Weekly, Days: []string{"mon", "thu"}}, true},
		{"empty time", ParseResult{Time: "", Date: "2026-01-05", Recurrence: Once}, false},
		{"non HHMM time", ParseResult{Time: "9:00", Date: "2026-01-05", Recurrence: Once}, false},
		{"hour out of range", ParseResult{Time: "99:99", Date: "2026-01-05", Recurrence: Once}, false},
		{"minute out of range", ParseResult{Time: "10:75", Date: "2026-01-05", Recurrence: Once}, false},
		{"bad date", ParseResult{Time: "09:00", Date: "2026-13-40", Recurrence: Once}, false},
		{"unknown recurrence", ParseResult{Time: "09:00", Date: "2026-01-05", Recurrence: Recurrence("yearly")}, false},
		{"long weekday name", ParseResult{Time: "09:00", Date: "2026-01-05", Recurrence: Weekly, Days: []string{"monday"}}, false},
	}
	for _, c := range cases {
		if got := c.r.valid(); got != c.want {
			t.Errorf("%s: valid() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHasTimeSignal(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"call the plumber", false},
		{"buy milk", false},
		{"call plumber at some point", true}, // "at" keyword
		{"meeting at 3", true},               // digit + keyword
		{"task 5", true},                     // bare digit
		{"sună mâine", true},                 // RO keyword
		{"citește în fiecare zi", true},      // RO phrase
		{"peste ceva timp", true},            // RO keyword "peste"
		{"read a book", false},
	}
	for _, c := range cases {
		if got := hasTimeSignal(c.text); got != c.want {
			t.Errorf("hasTimeSignal(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestParseDispatch(t *testing.T) {
	fakeOK := func(context.Context, string, time.Time) (ParseResult, error) {
		return ParseResult{Time: "15:00", Date: "2026-01-05", Recurrence: Once, Desc: "call plumber"}, nil
	}
	fakeErr := func(context.Context, string, time.Time) (ParseResult, error) {
		return ParseResult{}, errors.New("boom")
	}
	// Realistic unschedulable output: schema-valid shape (recurrence is a
	// required enum) but no time-of-day → must be floored.
	fakeNoTime := func(context.Context, string, time.Time) (ParseResult, error) {
		return ParseResult{Time: "", Date: "2026-01-05", Recurrence: Once}, nil
	}
	fakeGarbageTime := func(context.Context, string, time.Time) (ParseResult, error) {
		return ParseResult{Time: "99:99", Date: "2026-01-05", Recurrence: Once}, nil
	}
	fakeGarbageDays := func(context.Context, string, time.Time) (ParseResult, error) {
		return ParseResult{Time: "15:00", Date: "2026-01-05", Recurrence: Weekly, Days: []string{"monday"}}, nil
	}

	t.Run("heuristic hit skips haiku", func(t *testing.T) {
		called := false
		spy := func(ctx context.Context, s string, now time.Time) (ParseResult, error) {
			called = true
			return ParseResult{}, nil
		}
		_, dest, by := Parse("meeting at 14:30", parseNow, spy)
		if dest != "scheduled" || by != "heuristics" {
			t.Fatalf("dest/by = %s/%s, want scheduled/heuristics", dest, by)
		}
		if called {
			t.Fatal("haiku called despite heuristic hit")
		}
	})

	t.Run("no signal goes to inbox", func(t *testing.T) {
		called := false
		spy := func(ctx context.Context, s string, now time.Time) (ParseResult, error) {
			called = true
			return ParseResult{}, nil
		}
		_, dest, by := Parse("call the plumber", parseNow, spy)
		if dest != "inbox" || by != "none" {
			t.Fatalf("dest/by = %s/%s, want inbox/none", dest, by)
		}
		if called {
			t.Fatal("haiku called for a timeless capture")
		}
	})

	t.Run("signal + haiku success", func(t *testing.T) {
		res, dest, by := Parse("dinner at 8", parseNow, fakeOK)
		if dest != "scheduled" || by != "haiku" {
			t.Fatalf("dest/by = %s/%s, want scheduled/haiku", dest, by)
		}
		if res.Time != "15:00" {
			t.Fatalf("time = %q, want 15:00", res.Time)
		}
	})

	t.Run("pm-band bare hour routes to haiku", func(t *testing.T) {
		called := false
		spy := func(ctx context.Context, s string, now time.Time) (ParseResult, error) {
			called = true
			return ParseResult{Time: "20:00", Date: "2026-01-05", Recurrence: Once, Desc: "dinner"}, nil
		}
		_, dest, by := Parse("dinner at 8", parseNow, spy)
		if !called {
			t.Fatal("haiku not called for a bare pm-band hour")
		}
		if dest != "scheduled" || by != "haiku" {
			t.Fatalf("dest/by = %s/%s, want scheduled/haiku", dest, by)
		}
	})

	t.Run("signal + haiku error → inbox floor", func(t *testing.T) {
		_, dest, by := Parse("dinner at 8", parseNow, fakeErr)
		if dest != "inbox" || by != "none" {
			t.Fatalf("dest/by = %s/%s, want inbox/none", dest, by)
		}
	})

	t.Run("signal + haiku no-time → inbox floor", func(t *testing.T) {
		_, dest, by := Parse("dinner at 8", parseNow, fakeNoTime)
		if dest != "inbox" || by != "none" {
			t.Fatalf("dest/by = %s/%s, want inbox/none", dest, by)
		}
	})

	t.Run("signal + garbage time → inbox floor", func(t *testing.T) {
		_, dest, by := Parse("dinner at 8", parseNow, fakeGarbageTime)
		if dest != "inbox" || by != "none" {
			t.Fatalf("dest/by = %s/%s, want inbox/none", dest, by)
		}
	})

	t.Run("signal + garbage days → inbox floor", func(t *testing.T) {
		_, dest, by := Parse("dinner at 8", parseNow, fakeGarbageDays)
		if dest != "inbox" || by != "none" {
			t.Fatalf("dest/by = %s/%s, want inbox/none", dest, by)
		}
	})

	t.Run("signal + haiku timeout → inbox floor", func(t *testing.T) {
		blocking := func(ctx context.Context, s string, now time.Time) (ParseResult, error) {
			<-ctx.Done()
			return ParseResult{}, ctx.Err()
		}
		// Exercise the real WithTimeout wiring with a short deadline so the test
		// stays fast (no 3s wait).
		_, dest, by := parseWithTimeout("dinner at 8", parseNow, blocking, 20*time.Millisecond)
		if dest != "inbox" || by != "none" {
			t.Fatalf("dest/by = %s/%s, want inbox/none", dest, by)
		}
	})
}

func TestParseRecurringHeuristicSchedules(t *testing.T) {
	noHaiku := func(context.Context, string, time.Time) (ParseResult, error) {
		t.Fatal("haiku called for a heuristic hit")
		return ParseResult{}, nil
	}
	for _, text := range []string{"meditate every night at 22", "standup every weekday at 9:30"} {
		res, dest, by := Parse(text, parseNow, noHaiku)
		if dest != "scheduled" || by != "heuristics" || res.Date != "2026-01-05" {
			t.Errorf("%q: got %s/%s date %q, want scheduled/heuristics date 2026-01-05", text, dest, by, res.Date)
		}
	}
}
