package main

import (
	"fmt"
	"os"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "daemon" {
		home, _ := os.UserHomeDir()
		startCalSync(runtime.GOOS, home)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "add":
			cliAdd(os.Args[2:])
		case "list", "ls":
			cliList()
		case "archive":
			cliArchive()
		case "done":
			cliDone(os.Args[2:])
		case "capture":
			cliCapture(os.Args[2:])
		case "snooze":
			cliSnooze(os.Args[2:])
		case "clear":
			cliClear()
		case "status":
			cliStatus()
		case "daemon":
			runDaemon()
		case "nudge":
			cliNudge(os.Args[2:])
		case "cal-add":
			cliCalAdd(os.Args[2:])
		case "cal-remove":
			cliCalRemove(os.Args[2:])
		case "cal-feeds":
			cliCalFeeds()
		case "cal-list":
			cliCalList()
		case "cal-sync":
			cliCalSync(os.Args[2:])
		case "claude-install":
			cliClaudeInstall()
		case "opencode-install":
			cliOpencodeInstall()
		case "codex-install":
			cliCodexInstall()
		case "codex-guard":
			cliCodexGuard()
		case "help", "--help", "-h":
			printUsage()
		default:
			fmt.Printf("unknown command: %s\n", os.Args[1])
			printUsage()
			os.Exit(1)
		}
		return
	}

	// The TUI is a pure HTTP client + viewer. loadStore seeds the read-only
	// display cache (tasks + settings) from disk; each tick refreshes it from the
	// daemon over HTTP. The daemon (flow-breaker daemon) is the sole writer.
	s := loadStore()
	capturePane()

	m := model{store: s}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
