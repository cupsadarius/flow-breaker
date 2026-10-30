package main

import (
	"sort"
	"time"
)

type HabitCell string

const (
	CellDone   HabitCell = "done"
	CellMissed HabitCell = "missed"
	CellNone   HabitCell = "none"
)

// Habit is the recurring tasks that share one Desc, seen as one habit.
type Habit struct {
	Name          string      `json:"name"`
	TaskIDs       []int       `json:"task_ids"`
	Slots         []string    `json:"slots"`
	Streak        int         `json:"streak"`
	Cells         []HabitCell `json:"cells"` // 28 days; Cells[0] is the Monday 3 weeks before this week's Monday
	WeekDone      int         `json:"week_done"`
	WeekScheduled int         `json:"week_scheduled"`
}

type slotState int

const (
	slotOff slotState = iota // not scheduled that day
	slotDone
	slotMissed
	slotPending // today, not yet acted on
)

func slotOn(t Task, day, today time.Time) slotState {
	if !shouldFireOnDay(t, day.Weekday()) || day.Before(createdOnDate(t)) {
		return slotOff
	}
	if day.Equal(today) {
		switch {
		case t.Done:
			return slotDone
		case t.Dismissed || t.Missed:
			return slotMissed
		}
		return slotPending
	}
	if t.History[day.Format("2006-01-02")] {
		return slotDone
	}
	return slotMissed
}

// dayCell folds a habit's slots on one day into a grid cell. A dismissed or
// escalation-missed slot counts as missed.
func dayCell(tasks []Task, day, today time.Time) HabitCell {
	if day.After(today) {
		return CellNone
	}
	scheduled, pending := false, false
	for _, t := range tasks {
		switch slotOn(t, day, today) {
		case slotMissed:
			return CellMissed
		case slotPending:
			pending = true
			scheduled = true
		case slotDone:
			scheduled = true
		}
	}
	if !scheduled || pending {
		return CellNone
	}
	return CellDone
}

func buildHabits(tasks []Task, now time.Time) []Habit {
	today := startOfDay(now)
	groups := map[string][]Task{}
	var order []string
	for _, t := range tasks {
		if t.Archived || t.Recurrence == Once {
			continue
		}
		if _, ok := groups[t.Desc]; !ok {
			order = append(order, t.Desc)
		}
		groups[t.Desc] = append(groups[t.Desc], t)
	}

	monday := weekStart(now)
	first := monday.AddDate(0, 0, -21)
	var out []Habit
	for _, name := range order {
		ts := groups[name]
		sort.Slice(ts, func(i, j int) bool { return ts[i].Time < ts[j].Time })
		h := Habit{Name: name, Cells: make([]HabitCell, 28)}
		start := today
		for _, t := range ts {
			h.TaskIDs = append(h.TaskIDs, t.ID)
			h.Slots = append(h.Slots, t.Time)
			if c := createdOnDate(t); c.Before(start) {
				start = c
			}
		}
		for i := range h.Cells {
			h.Cells[i] = dayCell(ts, first.AddDate(0, 0, i), today)
		}
		for d := today; !d.Before(start) && today.Sub(d) <= 366*24*time.Hour; d = d.AddDate(0, 0, -1) {
			c := dayCell(ts, d, today)
			if c == CellMissed {
				break
			}
			if c == CellDone {
				h.Streak++
			}
		}
		for d := monday; !d.After(today); d = d.AddDate(0, 0, 1) {
			for _, t := range ts {
				switch slotOn(t, d, today) {
				case slotDone:
					h.WeekDone++
					h.WeekScheduled++
				case slotMissed, slotPending:
					h.WeekScheduled++
				}
			}
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Slots[0] < out[j].Slots[0] })
	return out
}

func weekStart(t time.Time) time.Time {
	wd := t.Weekday()
	if wd == time.Sunday {
		wd = 7
	}
	d := t.AddDate(0, 0, -int(wd-time.Monday))
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
}
