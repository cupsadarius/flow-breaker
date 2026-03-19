package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "add":
			cliAdd(os.Args[2:])
		case "list", "ls":
			cliList()
		case "done":
			cliDone(os.Args[2:])
		case "clear":
			cliClear()
		case "status":
			cliStatus()
		case "nudge":
			cliNudge()
		case "cal-add":
			cliCalAdd(os.Args[2:])
		case "cal-remove":
			cliCalRemove(os.Args[2:])
		case "cal-feeds":
			cliCalFeeds()
		case "cal-list":
			cliCalList()
		case "claude-install":
			cliClaudeInstall()
		case "opencode-install":
			cliOpencodeInstall()
		case "help", "--help", "-h":
			printUsage()
		default:
			fmt.Printf("unknown command: %s\n", os.Args[1])
			printUsage()
			os.Exit(1)
		}
		return
	}

	s := loadStore()
	s.resetDaily()
	alarm := &alarmState{}

	// start socket server
	startSocketServer(&s, alarm)
	// write initial status
	writeStatusFile(&s, alarm)

	m := model{
		store: &s,
		alarm: alarm,
		fired: make(map[int]bool),
	}

	m.checkOverdueOnStartup()

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// cleanup socket on exit
	os.Remove(sockPath())
}
