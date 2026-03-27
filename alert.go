package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ── tmux pane tracking ───────────────────────────────────────────────────────

var tmuxPaneID string

func captureTmuxPane() {
	tmuxPaneID = os.Getenv("TMUX_PANE")
}

// ── Alerts: macOS native + tmux + terminal bell ────────────────────────────

func alertAll(title, body string, cfg *Settings) {
	if cfg.AlertNotify {
		macNotify(title, body)
	}
	if cfg.AlertDialog {
		macAlert(title, body, cfg.SnoozeMins)
	}
	if cfg.AlertSpeech {
		macSay(body)
	}
	if cfg.AlertSound {
		macSound("Funk")
	}
	if cfg.AlertBell {
		fmt.Print("\a")
	}
	if cfg.AlertTmux {
		tmuxAlert(title + ": " + body)
	}
}

func macNotify(title, body string) {
	script := fmt.Sprintf(
		`display notification %q with title %q sound name "Glass"`,
		body, title,
	)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

var macAlertResult = make(chan string, 16)

func macAlert(title, body string, snoozeMins int) {
	script := fmt.Sprintf(
		`display alert %q message %q buttons {"Snooze %dm", "Done", "Dismiss"} default button "Dismiss" giving up after 300`,
		title, body, snoozeMins,
	)
	cmd := exec.Command("osascript", "-e", script)
	go func() {
		out, err := cmd.Output()
		if err == nil {
			macAlertResult <- strings.TrimSpace(string(out))
		}
	}()
}

func macSay(text string) {
	cmd := exec.Command("say", "-v", "Samantha", "-r", "200", text)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

func macSound(name string) {
	path := "/System/Library/Sounds/" + name + ".aiff"
	cmd := exec.Command("afplay", path)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

func tmuxAlert(msg string) {
	if os.Getenv("TMUX") == "" {
		return
	}

	target := tmuxPaneID

	// Switch to the flow-breaker window if not already visible.
	if target != "" {
		out, err := exec.Command("tmux", "display-message", "-t", target,
			"-p", "#{window_active}#{session_attached}").Output()
		if err == nil {
			state := strings.TrimSpace(string(out))
			if state != "11" {
				// Resolve session:window from our pane.
				sw, err := exec.Command("tmux", "display-message", "-t", target,
					"-p", "#{session_name}:#{window_index}").Output()
				if err == nil {
					sessionWindow := strings.TrimSpace(string(sw))
					exec.Command("tmux", "switch-client", "-t", sessionWindow).Run()
					exec.Command("tmux", "select-window", "-t", sessionWindow).Run()
				}
			}
		}
	}

	// Flash pane and show message, targeting our specific pane if known.
	if target != "" {
		cmd1 := exec.Command("tmux", "display-message", "-t", target, "-d", "5000", "⚡ "+msg)
		if err := cmd1.Start(); err == nil {
			go cmd1.Wait()
		}
		cmd2 := exec.Command("tmux", "select-pane", "-t", target, "-P", "bg=red")
		if err := cmd2.Start(); err == nil {
			go cmd2.Wait()
		}
		go func() {
			time.Sleep(3 * time.Second)
			cmd3 := exec.Command("tmux", "select-pane", "-t", target, "-P", "bg=default")
			if err := cmd3.Start(); err == nil {
				cmd3.Wait()
			}
		}()
	} else {
		cmd1 := exec.Command("tmux", "display-message", "-d", "5000", "⚡ "+msg)
		if err := cmd1.Start(); err == nil {
			go cmd1.Wait()
		}
		cmd2 := exec.Command("tmux", "select-pane", "-P", "bg=red")
		if err := cmd2.Start(); err == nil {
			go cmd2.Wait()
		}
		go func() {
			time.Sleep(3 * time.Second)
			cmd3 := exec.Command("tmux", "select-pane", "-P", "bg=default")
			if err := cmd3.Start(); err == nil {
				cmd3.Wait()
			}
		}()
	}
}

// ── Alarm state ────────────────────────────────────────────────────────────

type alarmState struct {
	active      bool
	taskIdx     int
	tick        int
	snoozeUntil int64
}

func (a *alarmState) trigger(idx int) {
	a.active = true
	a.taskIdx = idx
	a.tick = 0
}

func (a *alarmState) dismiss() {
	a.active = false
	a.taskIdx = -1
}

func (a *alarmState) snooze(minutes int) {
	a.snoozeUntil = time.Now().Add(time.Duration(minutes) * time.Minute).Unix()
	a.dismiss()
}
