package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
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

func cliCalAdd(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: flow-breaker cal-add <url-or-path> [--label \"Work\"]")
		os.Exit(1)
	}
	url := args[0]
	isFile := !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")

	label := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--label" && i+1 < len(args) {
			i++
			label = args[i]
		}
	}

	if isFile {
		// Resolve to absolute path for reliable storage
		abs, err := filepath.Abs(strings.TrimPrefix(url, "file://"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		// Validate file exists and contains iCal data
		fmt.Println("Validating file...")
		data, err := os.ReadFile(abs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !strings.Contains(string(data), "BEGIN:VCALENDAR") {
			fmt.Fprintf(os.Stderr, "error: file does not appear to be a valid iCal file\n")
			os.Exit(1)
		}
		url = abs
		if label == "" {
			label = filepath.Base(abs)
		}
	} else {
		// HTTP validation
		fmt.Println("Validating feed...")
		ical, err := fetchICalFeed(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !strings.Contains(ical, "BEGIN:VCALENDAR") {
			fmt.Fprintf(os.Stderr, "error: URL does not appear to be a valid iCal feed\n")
			os.Exit(1)
		}
		if label == "" {
			label = url
		}
	}

	if err := addFeed(url, label); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// enable calendar if not already
	s := loadStore()
	if !s.Settings.CalEnabled {
		s.Settings.CalEnabled = true
		s.save()
	}

	fmt.Printf("✓ Feed added: %s\n✓ Calendar enabled\n\nNext steps:\n  flow-breaker cal-list    see today's events\n  Launch TUI → press 'p'   import events as tasks\n", label)
}

func cliCalRemove(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: flow-breaker cal-remove <url-or-label>")
		os.Exit(1)
	}
	query := strings.Join(args, " ")
	if err := removeFeed(query); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Removed feed matching %q\n", query)
}

func cliCalFeeds() {
	feeds, err := loadFeeds()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(feeds) == 0 {
		fmt.Println("No feeds configured. Run: flow-breaker cal-add <url>")
		return
	}
	fmt.Printf("📅 Configured feeds (%d):\n\n", len(feeds))
	for _, f := range feeds {
		if f.Label != f.URL {
			fmt.Printf("  %-20s %s\n", f.Label, f.URL)
		} else {
			fmt.Printf("  %s\n", f.URL)
		}
	}
}

func cliCalList() {
	s := loadStore()
	feeds, err := loadFeeds()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(feeds) == 0 {
		fmt.Println("No feeds configured. Run: flow-breaker cal-add <url>")
		return
	}
	events, err := getCachedOrFetchEvents(feeds, s.Settings.CalCacheMins)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(events) == 0 {
		fmt.Println("No events today.")
		return
	}
	fmt.Printf("📅 Today's calendar events (%d):\n\n", len(events))
	for _, ev := range events {
		if ev.AllDay {
			fmt.Printf("  ALL DAY   %-40s  [%s]\n", ev.Summary, ev.CalendarName)
		} else {
			fmt.Printf("  %s─%s  %-40s  [%s]\n", ev.StartTime, ev.EndTime, ev.Summary, ev.CalendarName)
		}
	}
}

// ── Claude Code Integration ──────────────────────────────────────────────────

func cliClaudeInstall() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot create %s: %v\n", claudeDir, err)
		os.Exit(1)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	claudeMDPath := filepath.Join(claudeDir, "CLAUDE.md")

	patchSettings(settingsPath)
	patchClaudeMD(claudeMDPath)
}

func patchSettings(path string) {
	var settings map[string]interface{}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", path, err)
			os.Exit(1)
		}
		settings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &settings); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s is malformed JSON: %v\n", path, err)
			os.Exit(1)
		}
	}

	changed := false

	// ── SessionStart hook ──
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok {
		sessionStart = []interface{}{}
	}

	hookExists := false
	for _, entry := range sessionStart {
		entryMap, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		hooksList, ok := entryMap["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hooksList {
			hMap, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hMap["command"].(string)
			if strings.Contains(cmd, "flow-breaker nudge") {
				hookExists = true
				break
			}
		}
		if hookExists {
			break
		}
	}

	if hookExists {
		fmt.Println("✓ SessionStart hook already configured")
	} else {
		newEntry := map[string]interface{}{
			"matcher": "startup|resume",
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": "flow-breaker nudge 2>/dev/null || true",
				},
			},
		}
		sessionStart = append(sessionStart, newEntry)
		hooks["SessionStart"] = sessionStart
		changed = true
		fmt.Println("✓ added SessionStart hook to ~/.claude/settings.json")
	}

	// ── Bash permission for flow-breaker nudge ──
	const nudgePerm = "Bash(flow-breaker nudge*)"

	perms, ok := settings["permissions"].(map[string]interface{})
	if !ok {
		perms = make(map[string]interface{})
		settings["permissions"] = perms
	}

	allowList, ok := perms["allow"].([]interface{})
	if !ok {
		allowList = []interface{}{}
	}

	permExists := false
	for _, entry := range allowList {
		if s, ok := entry.(string); ok && s == nudgePerm {
			permExists = true
			break
		}
	}

	if permExists {
		fmt.Println("✓ Bash permission already configured")
	} else {
		allowList = append(allowList, nudgePerm)
		perms["allow"] = allowList
		changed = true
		fmt.Println("✓ added Bash permission for flow-breaker nudge")
	}

	// ── Write back if anything changed ──
	if !changed {
		return
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot marshal settings: %v\n", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if err := os.WriteFile(path, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", path, err)
		os.Exit(1)
	}
}

func patchClaudeMD(path string) {
	content := ""
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", path, err)
		os.Exit(1)
	}
	if err == nil {
		content = string(data)
	}

	// Check if already present (idempotent)
	if strings.Contains(content, "## flow-breaker") {
		fmt.Println("✓ CLAUDE.md already configured")
		return
	}

	section := `## flow-breaker

At the start of every conversation, run ` + "`flow-breaker nudge`" + ` and surface any alerts or overdue tasks to the user before proceeding. If there are overdue tasks, mention them prominently.

During long sessions, run ` + "`flow-breaker nudge`" + ` every 30 minutes to check for new overdue tasks. If the user asks you to monitor tasks, use ` + "`/loop 30m flow-breaker nudge`" + ` to automate this.
`

	// Ensure proper newline separation
	if content != "" && !strings.HasSuffix(content, "\n\n") {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n"
	}
	content += section

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Println("✓ added flow-breaker section to ~/.claude/CLAUDE.md")
}

// ── OpenCode Integration ───────────────────────────────────────────────────

func cliOpencodeInstall() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	opencodeDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot create %s: %v\n", opencodeDir, err)
		os.Exit(1)
	}

	configPath := filepath.Join(opencodeDir, "opencode.json")
	pluginsDir := filepath.Join(opencodeDir, "plugins")
	agentsMDPath := filepath.Join(opencodeDir, "AGENTS.md")

	patchOpencodeConfig(configPath)
	writeOpencodePlugin(pluginsDir)
	patchAgentsMD(agentsMDPath)
}

// stripJSONC removes trailing commas before } and ] so that JSONC
// files (as used by opencode) can be parsed with encoding/json.
func stripJSONC(data []byte) []byte {
	re := regexp.MustCompile(`,\s*([}\]])`)
	return re.ReplaceAll(data, []byte("$1"))
}

func patchOpencodeConfig(path string) {
	var config map[string]interface{}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", path, err)
			os.Exit(1)
		}
		config = map[string]interface{}{
			"$schema": "https://opencode.ai/config.json",
		}
	} else {
		clean := stripJSONC(data)
		if err := json.Unmarshal(clean, &config); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s is malformed JSON: %v\n", path, err)
			os.Exit(1)
		}
	}

	changed := false

	// ── Bash permission for flow-breaker nudge ──
	const nudgePattern = "flow-breaker nudge*"
	const nudgeAllow = "allow"

	perm, ok := config["permission"].(map[string]interface{})
	if !ok {
		perm = make(map[string]interface{})
		config["permission"] = perm
	}

	bash, ok := perm["bash"].(map[string]interface{})
	if !ok {
		bash = make(map[string]interface{})
		perm["bash"] = bash
	}

	if existing, ok := bash[nudgePattern].(string); ok && existing == nudgeAllow {
		fmt.Println("✓ bash permission already configured")
	} else {
		bash[nudgePattern] = nudgeAllow
		changed = true
		fmt.Println("✓ added bash permission for flow-breaker nudge")
	}

	if !changed {
		return
	}

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot marshal config: %v\n", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if err := os.WriteFile(path, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", path, err)
		os.Exit(1)
	}
}

const opencodePluginJS = `export const FlowBreaker = async ({ $ }) => {
  let cached = ""

  const refresh = async () => {
    try {
      cached = await $` + "`flow-breaker nudge`" + `.quiet().nothrow().text()
    } catch {
      cached = ""
    }
  }
  await refresh()
  setInterval(refresh, 30 * 60 * 1000)

  return {
    "experimental.chat.system.transform": async (input, output) => {
      if (cached.trim()) {
        output.system.push("## flow-breaker alerts\n" + cached.trim())
      }
    },
  }
}
`

func writeOpencodePlugin(pluginsDir string) {
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot create %s: %v\n", pluginsDir, err)
		os.Exit(1)
	}

	pluginPath := filepath.Join(pluginsDir, "flow-breaker.js")

	if _, err := os.Stat(pluginPath); err == nil {
		fmt.Println("✓ opencode plugin already installed")
		return
	}

	if err := os.WriteFile(pluginPath, []byte(opencodePluginJS), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", pluginPath, err)
		os.Exit(1)
	}
	fmt.Println("✓ created opencode plugin at ~/.config/opencode/plugins/flow-breaker.js")
}

func patchAgentsMD(path string) {
	content := ""
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", path, err)
		os.Exit(1)
	}
	if err == nil {
		content = string(data)
	}

	if strings.Contains(content, "## flow-breaker") {
		fmt.Println("✓ AGENTS.md already configured")
		return
	}

	section := `## flow-breaker

At the start of every conversation, run ` + "`flow-breaker nudge`" + ` and surface any alerts or overdue tasks to the user before proceeding. If there are overdue tasks, mention them prominently.

During long sessions, run ` + "`flow-breaker nudge`" + ` every 30 minutes to check for new overdue tasks.
`

	if content != "" && !strings.HasSuffix(content, "\n\n") {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n"
	}
	content += section

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Println("✓ added flow-breaker section to ~/.config/opencode/AGENTS.md")
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

CALENDAR:
  flow-breaker cal-add <url|path>  add an iCal feed URL or local .ics file (--label optional)
  flow-breaker cal-remove <q>   remove a feed by URL or label
  flow-breaker cal-feeds        list configured feeds
  flow-breaker cal-list         show today's calendar events

SETUP:
  flow-breaker claude-install    install Claude Code hook + instructions
  flow-breaker opencode-install  install opencode plugin + instructions

INTEGRATION:
  Socket:  ~/.flow-breaker/flow.sock  (send: status|nudge|next|overdue|alarm|calendar)
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
  p   calendar events   o      settings
  j/k navigate          q      quit

ALERTS (macOS):
  Notification Center · modal dialog · text-to-speech
  system sound · terminal bell · tmux pane flash`)
}
