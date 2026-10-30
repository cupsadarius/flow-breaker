package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed macos/FlowBreakerSync/main.swift
var syncSwift []byte

//go:embed macos/FlowBreakerSync/Info.plist
var syncPlist []byte

const syncAppRel = "Applications/FlowBreakerSync.app"

// openApp starts the helper in the background; `open` does nothing when the
// app already runs. It does not wait, so the nudge hook stays fast.
var openApp = func(path string) error {
	cmd := exec.Command("open", "-g", "-j", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// startCalSync starts FlowBreakerSync.app when it is installed (macOS only).
func startCalSync(goos, home string) {
	if goos != "darwin" {
		return
	}
	app := filepath.Join(home, syncAppRel)
	if _, err := os.Stat(app); err == nil {
		openApp(app)
	}
}

// cliCalSync is `flow-breaker cal-sync [--install]`: install the helper, or
// print its sync status.
func cliCalSync(args []string) {
	home, _ := os.UserHomeDir()
	if len(args) > 0 && args[0] == "--install" {
		if err := installCalSync(home); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(`✓ FlowBreakerSync.app installed and started.

Next steps:
  1. Accept the calendar access prompt.
  2. System Settings → Internet Accounts: the Google account needs Calendars on.
  3. flow-breaker cal-remove "Optura AI"   drop the downloaded .ics feed
  4. flow-breaker cal-sync                 check the sync status`)
		return
	}
	st, ok := readCalSync()
	if !ok {
		fmt.Println("calendar sync: not installed (run flow-breaker cal-sync --install)")
		return
	}
	fmt.Printf("last success: %s\nlast attempt: %s\nevents today: %d\n", orNever(st.LastSuccess), orNever(st.LastAttempt), st.Events)
	if w := calSyncWarning(st, time.Now()); w != "" {
		fmt.Println("⚠️ " + w)
	}
}

func orNever(ts string) string {
	if ts == "" {
		return "never"
	}
	return ts
}

// installCalSync builds the helper from the embedded source and ad-hoc signs
// it. Each build is a new code identity, so macOS asks for access again.
func installCalSync(home string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("cal-sync needs macOS")
	}
	tmp, err := os.MkdirTemp("", "fbsync")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "main.swift")
	if err := os.WriteFile(src, syncSwift, 0o644); err != nil {
		return err
	}
	app := filepath.Join(home, syncAppRel)
	bin := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	swiftc := exec.Command("swiftc", "-O", "-framework", "EventKit", "-framework", "AppKit", "-o", filepath.Join(bin, "FlowBreakerSync"), src)
	if out, err := swiftc.CombinedOutput(); err != nil {
		return fmt.Errorf("swiftc: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), syncPlist, 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("codesign", "--force", "-s", "-", app).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign: %v\n%s", err, out)
	}
	exec.Command("pkill", "-x", "FlowBreakerSync").Run() // a running old build keeps its old code
	return openApp(app)
}

// calSyncStatus is calsync.json, written by FlowBreakerSync.app after every
// sync attempt.
type calSyncStatus struct {
	LastAttempt string `json:"last_attempt"`
	LastSuccess string `json:"last_success"`
	Events      int    `json:"events"`
	Error       string `json:"error"`
}

func readCalSync() (calSyncStatus, bool) {
	var st calSyncStatus
	data, err := os.ReadFile(filepath.Join(dataDir(), "calsync.json"))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, true
}

// calSyncWarning returns a one-line warning, or "" when sync is healthy or the
// helper only just started (a fresh attempt after sleep).
func calSyncWarning(st calSyncStatus, now time.Time) string {
	if strings.Contains(st.Error, "access") {
		return "calendar access denied — System Settings → Privacy → Calendars"
	}
	within := func(ts string, d time.Duration) bool {
		t, err := time.Parse(time.RFC3339, ts)
		return err == nil && now.Sub(t) < d
	}
	if within(st.LastSuccess, 30*time.Minute) {
		return ""
	}
	if st.Error != "" {
		return "calendar sync failing: " + st.Error
	}
	if within(st.LastAttempt, 5*time.Minute) {
		return ""
	}
	since := "never"
	if t, err := time.Parse(time.RFC3339, st.LastSuccess); err == nil {
		since = t.Local().Format("15:04")
	}
	return "calendar sync stale since " + since
}

// calSyncNotice is the warning for the current status file; "" when the
// helper is not installed.
func calSyncNotice() string {
	st, ok := readCalSync()
	if !ok {
		return ""
	}
	return calSyncWarning(st, time.Now())
}
