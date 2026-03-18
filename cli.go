package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// ── CLI Commands ───────────────────────────────────────────────────────────

func cliAdd(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: flow-breaker add <HH:MM> <desc> [--repeat daily|weekdays|weekly|monthly|once] [--tags a,b]")
		os.Exit(1)
	}
	timeStr := args[0]
	if _, ok := parseTaskTime(timeStr); !ok {
		fmt.Printf("invalid time: %s (use HH:MM)\n", timeStr)
		os.Exit(1)
	}
	rec := Daily
	var tags []string
	var descParts []string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--repeat", "-r":
			if i+1 < len(args) {
				i++
				rec = Recurrence(args[i])
			}
		case "--tags", "-t":
			if i+1 < len(args) {
				i++
				for _, t := range strings.Split(args[i], ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						tags = append(tags, t)
					}
				}
			}
		default:
			descParts = append(descParts, args[i])
		}
	}
	desc := strings.Join(descParts, " ")
	s := loadStore()
	s.resetDaily()
	t := s.addTask(timeStr, desc, rec, tags, nil)
	fmt.Printf("✓ added [%d] %s %s (%s)\n", t.ID, t.Time, t.Desc, t.Recurrence)
}

func cliList() {
	s := loadStore()
	s.resetDaily()
	if len(s.Tasks) == 0 {
		fmt.Println("No tasks.")
		return
	}
	for _, t := range s.Tasks {
		icon := " "
		if t.Done {
			icon = "✓"
		}
		tags := ""
		if len(t.Tags) > 0 {
			tags = " [" + strings.Join(t.Tags, ",") + "]"
		}
		fmt.Printf(" %s %5s  %-35s %-10s%s\n", icon, t.Time, t.Desc, t.Recurrence, tags)
	}
}

func cliDone(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: flow-breaker done <HH:MM or description substring>")
		os.Exit(1)
	}
	q := strings.ToLower(strings.Join(args, " "))
	s := loadStore()
	for i, t := range s.Tasks {
		if strings.ToLower(t.Time) == q || strings.Contains(strings.ToLower(t.Desc), q) {
			s.Tasks[i].Done = true
			s.save()
			fmt.Printf("✓ done: %s %s\n", t.Time, t.Desc)
			return
		}
	}
	fmt.Println("no matching task found")
}

func cliClear() {
	s := Store{NextID: 1}
	s.save()
	fmt.Println("✓ all tasks cleared")
}

func cliStatus() {
	s := loadStore()
	s.resetDaily()
	report := buildStatus(&s, &alarmState{})
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
}

func cliNudge() {
	// try socket first (gets live alarm state from running TUI)
	conn, err := net.DialTimeout("unix", sockPath(), 2*time.Second)
	if err == nil {
		defer conn.Close()
		conn.Write([]byte("nudge"))
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err == nil {
			fmt.Print(string(buf[:n]))
			return
		}
	}
	// fallback: read status file
	data, err := os.ReadFile(statusPath())
	if err != nil {
		// no TUI running, build from tasks file
		s := loadStore()
		s.resetDaily()
		report := buildStatus(&s, &alarmState{})
		fmt.Println(report.Nudge)
		return
	}
	var report StatusReport
	json.Unmarshal(data, &report)
	fmt.Println(report.Nudge)
}

func printUsage() {
	fmt.Println(`flow-breaker — terminal daily planner that breaks your flow

USAGE:
  flow-breaker              launch TUI
  flow-breaker add <args>   add a task from CLI
  flow-breaker list         list today's tasks
  flow-breaker done <q>     mark a task done
  flow-breaker clear        delete all tasks
  flow-breaker status       JSON status (for scripts)
  flow-breaker nudge        one-liner for Claude Code

INTEGRATION:
  Socket:  ~/.flow-breaker/flow.sock  (send: status|nudge|next|overdue|alarm)
  File:    ~/.flow-breaker/status.json (updated every 500ms while TUI runs)

  # from Claude Code / any script:
  echo "nudge" | nc -U ~/.flow-breaker/flow.sock
  cat ~/.flow-breaker/status.json | jq .nudge
  flow-breaker nudge

ADD EXAMPLES:
  flow-breaker add 09:00 "Stand-up" --repeat weekdays
  flow-breaker add 12:30 "Lunch" --repeat daily --tags health
  flow-breaker add 14:00 "Call plumber" --repeat once

TUI KEYS:
  a   add task          SPACE  dismiss alarm
  d   delete task       s      snooze (configurable)
  c   toggle done       r      reload
  e   edit task         h      habit view
  o   settings          q      quit
  j/k navigate

ALERTS (macOS):
  Notification Center · modal dialog · text-to-speech
  system sound · terminal bell · tmux pane flash`)
}
