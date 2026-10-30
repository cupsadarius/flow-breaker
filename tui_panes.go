package main

import (
	"sort"
	"time"
)

// TodayGroups splits today's tasks by state, each group in time order.
type TodayGroups struct{ Overdue, Next, Later, Done []Task }

func groupToday(tasks []Task, now time.Time) TodayGroups {
	var today []Task
	for _, t := range tasks {
		if shouldFireTodayAt(t, now) {
			today = append(today, t)
		}
	}
	sort.SliceStable(today, func(i, j int) bool { return timeUntilAt(today[i], now) < timeUntilAt(today[j], now) })

	var g TodayGroups
	for _, t := range today {
		switch {
		case t.Done || t.Dismissed:
			g.Done = append(g.Done, t)
		case timeUntilAt(t, now) < 0:
			g.Overdue = append(g.Overdue, t)
		case len(g.Next) == 0:
			g.Next = append(g.Next, t)
		default:
			g.Later = append(g.Later, t)
		}
	}
	return g
}
