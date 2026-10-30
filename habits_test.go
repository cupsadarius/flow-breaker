package main

import (
	"testing"
	"time"
)

// habitNow is Friday 2026-10-30 14:00 local; this week's Monday is 10-26, so
// Cells[0] is Monday 10-05 and Cells[25] is today.
var habitNow = time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)

func created(y, m, d int) string {
	return time.Date(y, time.Month(m), d, 9, 0, 0, 0, time.Local).Format(time.RFC3339)
}

func history(days ...string) map[string]bool {
	h := map[string]bool{}
	for _, d := range days {
		h[d] = true
	}
	return h
}

func TestBuildHabitsGroupsByDesc(t *testing.T) {
	tasks := []Task{
		{ID: 1, Time: "16:30", Desc: "push-ups", Recurrence: Daily, CreatedAt: created(2026, 10, 1)},
		{ID: 2, Time: "10:00", Desc: "push-ups", Recurrence: Daily, CreatedAt: created(2026, 10, 1)},
		{ID: 3, Time: "12:00", Desc: "call plumber", Recurrence: Once, CreatedAt: created(2026, 10, 30)},
	}
	hs := buildHabits(tasks, habitNow)
	if len(hs) != 1 {
		t.Fatalf("habits = %d, want 1: %+v", len(hs), hs)
	}
	h := hs[0]
	if h.Name != "push-ups" || len(h.Slots) != 2 || h.Slots[0] != "10:00" || h.Slots[1] != "16:30" {
		t.Fatalf("habit = %+v", h)
	}
	if len(h.Cells) != 28 {
		t.Fatalf("cells = %d, want 28", len(h.Cells))
	}
}

func TestStreakCountsDoneDaysAndSkipsPendingToday(t *testing.T) {
	tasks := []Task{{ID: 1, Time: "18:00", Desc: "read", Recurrence: Daily, CreatedAt: created(2026, 10, 20),
		History: history("2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29")}}
	h := buildHabits(tasks, habitNow)[0]
	if h.Streak != 4 {
		t.Fatalf("streak = %d, want 4", h.Streak)
	}
	if h.Cells[24] != CellDone || h.Cells[25] != CellNone {
		t.Fatalf("thu = %s today = %s, want done/none", h.Cells[24], h.Cells[25])
	}
}

func TestDismissedTodayBreaksStreak(t *testing.T) {
	tasks := []Task{{ID: 1, Time: "10:00", Desc: "read", Recurrence: Daily, CreatedAt: created(2026, 10, 20),
		Dismissed: true, History: history("2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29")}}
	h := buildHabits(tasks, habitNow)[0]
	if h.Streak != 0 || h.Cells[25] != CellMissed {
		t.Fatalf("streak = %d today = %s, want 0/missed", h.Streak, h.Cells[25])
	}
}

func TestPartialSlotsTodayIsNone(t *testing.T) {
	tasks := []Task{
		{ID: 1, Time: "10:00", Desc: "squats", Recurrence: Daily, CreatedAt: created(2026, 10, 29), Done: true, History: history("2026-10-29")},
		{ID: 2, Time: "15:00", Desc: "squats", Recurrence: Daily, CreatedAt: created(2026, 10, 29), History: history("2026-10-29")},
	}
	h := buildHabits(tasks, habitNow)[0]
	if h.Cells[25] != CellNone || h.Streak != 1 {
		t.Fatalf("today = %s streak = %d, want none/1", h.Cells[25], h.Streak)
	}
}

func TestDaysBeforeCreationAreNone(t *testing.T) {
	tasks := []Task{{ID: 1, Time: "10:00", Desc: "stretch", Recurrence: Daily, CreatedAt: created(2026, 10, 28)}}
	h := buildHabits(tasks, habitNow)[0]
	if h.Cells[22] != CellNone { // 10-27
		t.Fatalf("10-27 = %s, want none", h.Cells[22])
	}
	if h.Cells[23] != CellMissed || h.Cells[24] != CellMissed { // 10-28, 10-29
		t.Fatalf("10-28 = %s 10-29 = %s, want missed", h.Cells[23], h.Cells[24])
	}
	if h.Streak != 0 {
		t.Fatalf("streak = %d, want 0", h.Streak)
	}
}

func TestWeekdaysHabitSkipsWeekend(t *testing.T) {
	var days []string
	for d := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local); d.Day() <= 29 && d.Month() == 10; d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			days = append(days, d.Format("2006-01-02"))
		}
	}
	tasks := []Task{{ID: 1, Time: "16:30", Desc: "push-ups", Recurrence: Weekdays, CreatedAt: created(2026, 10, 1), History: history(days...)}}
	h := buildHabits(tasks, habitNow)[0]
	if h.Streak != 21 {
		t.Fatalf("streak = %d, want 21", h.Streak)
	}
	if h.Cells[19] != CellNone { // Sat 10-24
		t.Fatalf("sat = %s, want none", h.Cells[19])
	}
	if h.WeekScheduled != 5 || h.WeekDone != 4 {
		t.Fatalf("week = %d/%d, want 4/5", h.WeekDone, h.WeekScheduled)
	}
}
