package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// calSecretURLHint teaches the user where to find their private iCal URL. It is
// printed on `cal-add --help`, on a bare `cal-add` with no URL, and once in the
// success output after a feed is added.
const calSecretURLHint = "Google Calendar → Settings → (select the calendar) → **Integrate calendar** → **Secret address in iCal format**. Treat the URL like a password — anyone holding it can read the calendar."

// errCalAddUsage signals a cal-add usage error (no URL). The hint + usage are
// already written to the output writer, so the wrapper just needs a non-zero
// exit without printing a redundant "error:" line.
var errCalAddUsage = errors.New("cal-add: missing url argument")

// ── CLI Commands ───────────────────────────────────────────────────────────

// cliFail renders a client error and exits. ErrDaemonDown gets a friendly hint
// so users understand a running daemon is now required for CLI operations.
func cliFail(err error) {
	if errors.Is(err, ErrDaemonDown) {
		fmt.Fprintln(os.Stderr, "⚠️ flow-breaker unreachable (is the daemon running? `flow-breaker daemon`)")
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	os.Exit(1)
}

// taskResp is the {ok, task} envelope returned by mutation endpoints.
type taskResp struct {
	OK   bool `json:"ok"`
	Task Task `json:"task"`
}

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
	body := map[string]any{
		"time":       timeStr,
		"desc":       desc,
		"recurrence": string(rec),
		"tags":       tags,
		"source":     cliSource(),
	}
	var resp taskResp
	if err := newClient().Post("/task", body, &resp); err != nil {
		cliFail(err)
	}
	t := resp.Task
	fmt.Printf("✓ added [%d] %s %s (%s)\n", t.ID, t.Time, t.Desc, t.Recurrence)
}

func cliList() {
	var active []Task
	if err := newClient().Get("/tasks", &active); err != nil {
		cliFail(err)
	}
	if len(active) == 0 {
		fmt.Println("No tasks.")
		return
	}
	for _, t := range active {
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

func cliArchive() {
	var archived []Task
	if err := newClient().Get("/tasks?scope=archived", &archived); err != nil {
		cliFail(err)
	}
	if len(archived) == 0 {
		fmt.Println("No archived tasks.")
		return
	}
	fmt.Printf("📦 Archived tasks (%d):\n\n", len(archived))
	for _, t := range archived {
		icon := " "
		if t.Done {
			icon = "✓"
		} else if t.Dismissed {
			icon = "–"
		}
		tags := ""
		if len(t.Tags) > 0 {
			tags = " [" + strings.Join(t.Tags, ",") + "]"
		}
		archivedDate := ""
		if t.ArchivedAt != "" {
			if parsed, err := time.Parse(time.RFC3339, t.ArchivedAt); err == nil {
				archivedDate = parsed.Format("Jan 02")
			}
		}
		fmt.Printf(" %s %5s  %-30s%s  %s\n", icon, t.Time, t.Desc, tags, archivedDate)
	}
}

// doneByID marks the task done via the daemon and prints the result.
func doneByID(c *Client, id int) error {
	var resp taskResp
	if err := c.Post(fmt.Sprintf("/task/%d/done", id), map[string]any{"source": cliSource()}, &resp); err != nil {
		return err
	}
	fmt.Printf("✓ done: %s %s\n", resp.Task.Time, resp.Task.Desc)
	return nil
}

func cliDone(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: flow-breaker done <id | HH:MM | description substring>")
		fmt.Println("note: an all-digits arg is treated as a task ID, not a substring")
		os.Exit(1)
	}
	if err := doneWithClient(newClient(), args); err != nil {
		cliFail(err)
	}
}

// doneWithClient resolves args to a task and marks it done over c. A single
// all-digits arg is treated as a task ID (v2 ID-preference); otherwise the
// joined args are matched as an HH:MM/substring query client-side, matching v1.
func doneWithClient(c *Client, args []string) error {
	if len(args) == 1 {
		if id, err := strconv.Atoi(args[0]); err == nil {
			return doneByID(c, id)
		}
	}
	q := strings.ToLower(strings.Join(args, " "))
	var tasks []Task
	if err := c.Get("/tasks", &tasks); err != nil {
		return err
	}
	for _, t := range tasks {
		if strings.ToLower(t.Time) == q || strings.Contains(strings.ToLower(t.Desc), q) {
			return doneByID(c, t.ID)
		}
	}
	fmt.Println("no matching task found")
	return nil
}

func cliClear() {
	n, err := clearWithClient(newClient())
	if err != nil {
		cliFail(err)
	}
	fmt.Printf("✓ cleared %d task(s)\n", n)
}

// clearWithClient deletes every task (active and archived, per the documented
// "delete all tasks" contract) over c and returns the count deleted. A task
// that 404s mid-loop (concurrently removed by the TUI/daemon) is counted as
// already gone and skipped; only a transport failure (ErrDaemonDown) aborts.
func clearWithClient(c *Client) (int, error) {
	var tasks []Task
	if err := c.Get("/tasks?scope=all", &tasks); err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tasks {
		err := c.Delete(fmt.Sprintf("/task/%d", t.ID), map[string]any{"source": cliSource()}, nil)
		if err != nil {
			if errors.Is(err, ErrDaemonDown) {
				return n, err
			}
			// Concurrent delete (404 / not found): already gone, keep going.
			continue
		}
		n++
	}
	return n, nil
}

func cliStatus() {
	var report StatusReport
	if err := newClient().Get("/status", &report); err != nil {
		cliFail(err)
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
}

// cliCapture is `flow-breaker capture <text>` — the marquee "capture from the
// agent" path. It fails LOUD: an unreachable daemon prints to stderr and exits
// non-zero so a dropped capture is never silently swallowed.
func cliCapture(args []string) {
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		fmt.Println("usage: flow-breaker capture <text>")
		os.Exit(1)
	}
	if err := captureWithClient(newClient(), text, os.Stdout); err != nil {
		if errors.Is(err, ErrDaemonDown) {
			fmt.Fprintln(os.Stderr, "⚠️ flow-breaker unreachable — not captured")
			os.Exit(1)
		}
		cliFail(err)
	}
}

// captureWithClient POSTs free text to /capture over c and prints the daemon's
// echo (the human-readable summary of where the capture landed). Errors are
// returned, not printed, so the caller decides how loudly to fail.
func captureWithClient(c *Client, text string, out io.Writer) error {
	var resp struct {
		Echo string `json:"echo"`
	}
	if err := c.Post("/capture", map[string]any{"text": text, "source": cliSource()}, &resp); err != nil {
		return err
	}
	fmt.Fprintln(out, resp.Echo)
	return nil
}

// cliSnooze is `flow-breaker snooze <id> [minutes]` — parallels cliDone.
func cliSnooze(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: flow-breaker snooze <id> [minutes]")
		os.Exit(1)
	}
	if err := snoozeWithClient(newClient(), args, os.Stdout); err != nil {
		cliFail(err)
	}
}

// snoozeWithClient POSTs /task/{id}/snooze over c. The first arg is the task ID;
// an optional second arg overrides the snooze minutes (else the daemon uses
// Settings.SnoozeMins).
func snoozeWithClient(c *Client, args []string, out io.Writer) error {
	id, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid id: %s (snooze takes a task ID)", args[0])
	}
	body := map[string]any{"source": cliSource()}
	if len(args) >= 2 {
		mins, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid minutes: %s", args[1])
		}
		body["minutes"] = mins
	}
	var resp taskResp
	if err := c.Post(fmt.Sprintf("/task/%d/snooze", id), body, &resp); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ snoozed: %s %s\n", resp.Task.Time, resp.Task.Desc)
	return nil
}

// cliNudge is the `flow-breaker nudge [--quiet]` entry point. It uses an 800ms
// client so a slow/down daemon can never block the agent's turn, and always
// exits 0 (the loud path prints an honest unreachable line; the quiet path stays
// silent). --quiet is the shared primitive the opencode/Codex integrations use.
func cliNudge(args []string) {
	quiet := false
	for _, a := range args {
		if a == "--quiet" {
			quiet = true
		}
	}
	nudgeWithClient(newClientTimeout(800*time.Millisecond), quiet, os.Stdout)
}

// nudgeWithClient GETs the daemon's nudge text over c and writes it to out.
//
// Loud (quiet=false): on ANY failure (ErrDaemonDown/timeout/other) it prints an
// HONEST "unreachable" line — never a false "✅ All clear" — and returns nil
// (exit 0). On success it prints the body, which may honestly be the daemon's
// own all-clear (that's fine: the daemon is reachable).
//
// Quiet (quiet=true): silent on unreachable/timeout/empty body (fail-open, exit
// 0); prints only a non-empty gated nudge. It never blocks the turn.
func nudgeWithClient(c *Client, quiet bool, out io.Writer) error {
	path := "/nudge"
	if quiet {
		path = "/nudge?quiet=1"
	}
	body, err := c.GetText(path)
	if err != nil {
		if quiet {
			return nil // fail-open: say nothing
		}
		// Loud: honest unreachable, never a false all-clear.
		fmt.Fprintln(out, "⚠️ flow-breaker unreachable — tasks unknown")
		return nil
	}
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return nil // nothing to say (quiet all-clear, or an empty loud body)
	}
	fmt.Fprintln(out, body)
	return nil
}

// cliSource resolves the source attribution stamped on CLI mutations. An
// explicit FLOW_BREAKER_SOURCE wins; otherwise Claude Code (which sets CLAUDECODE)
// is detected as "claude-code"; else it is a plain "cli" invocation.
func cliSource() string {
	if s := os.Getenv("FLOW_BREAKER_SOURCE"); s != "" {
		return s
	}
	if os.Getenv("CLAUDECODE") != "" {
		return "claude-code"
	}
	return "cli"
}

// cliCalAdd is the thin os.Exit wrapper over calAdd. A usage error exits 1 with
// no extra output (the hint + usage were already printed); any other error is
// rendered as an "error:" line before exiting 1.
func cliCalAdd(args []string) {
	if err := calAdd(args, os.Stdout); err != nil {
		if !errors.Is(err, errCalAddUsage) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}
}

// calAdd runs the cal-add logic, writing all user-facing output to out and
// returning an error instead of calling os.Exit (so it is testable). It prints
// the secret-URL hint in three cases: --help, a bare invocation (no URL), and
// once after a successful add.
func calAdd(args []string, out io.Writer) error {
	// Help and the no-URL usage error both print usage + the secret-URL hint.
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, "usage: flow-breaker cal-add <url-or-path> [--label \"Work\"]")
		fmt.Fprintln(out, calSecretURLHint)
		if len(args) >= 1 { // --help / -h is an intentional, successful invocation
			return nil
		}
		return errCalAddUsage
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
			return err
		}
		// Validate file exists and contains iCal data
		fmt.Fprintln(out, "Validating file...")
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "BEGIN:VCALENDAR") {
			return errors.New("file does not appear to be a valid iCal file")
		}
		url = abs
		if label == "" {
			label = filepath.Base(abs)
		}
	} else {
		// HTTP validation
		fmt.Fprintln(out, "Validating feed...")
		ical, err := fetchICalFeed(url)
		if err != nil {
			return err
		}
		if !strings.Contains(ical, "BEGIN:VCALENDAR") {
			return errors.New("URL does not appear to be a valid iCal feed")
		}
		if label == "" {
			label = url
		}
	}

	if err := addFeed(url, label); err != nil {
		return err
	}

	// enable calendar if not already
	s := loadStore()
	if !s.Settings.CalEnabled {
		s.Settings.CalEnabled = true
		s.save()
	}

	fmt.Fprintf(out, "✓ Feed added: %s\n✓ Calendar enabled\n\nNext steps:\n  flow-breaker cal-list    see today's events\n  Launch TUI → press 'p'   import events as tasks\n", label)
	fmt.Fprintln(out, calSecretURLHint)
	return nil
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

	// ── Hooks ──
	// SessionStart: one loud nudge at the top of the session.
	// UserPromptSubmit: a gated, silent quiet nudge every turn (the 800ms
	// fail-open lives inside `flow-breaker nudge`, so a down daemon never blocks).
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	if hookInstalled(hooks, "SessionStart", "flow-breaker nudge") {
		fmt.Println("✓ SessionStart hook already configured")
	} else {
		appendHook(hooks, "SessionStart", "startup|resume", "flow-breaker nudge 2>/dev/null || true")
		changed = true
		fmt.Println("✓ added SessionStart hook to ~/.claude/settings.json")
	}

	if hookInstalled(hooks, "UserPromptSubmit", "flow-breaker nudge --quiet") {
		fmt.Println("✓ UserPromptSubmit hook already configured")
	} else {
		appendHook(hooks, "UserPromptSubmit", "", "flow-breaker nudge --quiet 2>/dev/null || true")
		changed = true
		fmt.Println("✓ added UserPromptSubmit hook to ~/.claude/settings.json")
	}

	// ── Granular, fail-closed Bash permissions ──
	// Claude Code precedence is deny > ask > allow, and there is NO wildcard, so
	// any subcommand not listed here (or a future one) falls through to the
	// default prompt rather than being silently auto-allowed.
	// The allow-list names ONLY subcommands main.go actually dispatches (reads +
	// capture + add); a subcommand not listed here falls through to the default
	// prompt rather than being silently auto-allowed.
	allowPerms := []string{
		"Bash(flow-breaker nudge:*)",
		"Bash(flow-breaker status:*)",
		"Bash(flow-breaker list:*)",
		"Bash(flow-breaker ls:*)",
		"Bash(flow-breaker archive:*)",
		"Bash(flow-breaker cal-list:*)",
		"Bash(flow-breaker cal-feeds:*)",
		"Bash(flow-breaker capture:*)",
		"Bash(flow-breaker add:*)",
	}
	askPerms := []string{
		"Bash(flow-breaker done:*)",
		"Bash(flow-breaker snooze:*)",
	}
	denyPerms := []string{
		"Bash(flow-breaker clear:*)",
	}

	perms, ok := settings["permissions"].(map[string]interface{})
	if !ok {
		perms = make(map[string]interface{})
		settings["permissions"] = perms
	}

	// Prune the pre-v2 no-colon pattern so upgraded users don't accumulate both
	// it and the granular Bash(flow-breaker nudge:*) below.
	if prunePermList(perms, "allow", "Bash(flow-breaker nudge*)") {
		changed = true
	}

	if mergePermList(perms, "allow", allowPerms) {
		changed = true
		fmt.Println("✓ merged allow permissions (reads + capture + add)")
	} else {
		fmt.Println("✓ allow permissions already configured")
	}
	if mergePermList(perms, "ask", askPerms) {
		changed = true
		fmt.Println("✓ merged ask permissions (done, snooze)")
	} else {
		fmt.Println("✓ ask permissions already configured")
	}
	if mergePermList(perms, "deny", denyPerms) {
		changed = true
		fmt.Println("✓ merged deny permissions (clear)")
	} else {
		fmt.Println("✓ deny permissions already configured")
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

// hookInstalled reports whether any command hook under the given event already
// contains marker, so re-running the install doesn't duplicate it.
func hookInstalled(hooks map[string]interface{}, event, marker string) bool {
	entries, ok := hooks[event].([]interface{})
	if !ok {
		return false
	}
	for _, entry := range entries {
		em, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		list, ok := em["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range list {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, marker) {
				return true
			}
		}
	}
	return false
}

// appendHook appends a command hook entry to the given event array, preserving
// any existing entries (the user's other hooks are never clobbered). An empty
// matcher is omitted (UserPromptSubmit takes no matcher).
func appendHook(hooks map[string]interface{}, event, matcher, command string) {
	entries, _ := hooks[event].([]interface{})
	entry := map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{"type": "command", "command": command},
		},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	hooks[event] = append(entries, entry)
}

// mergePermList merges adds into perms[key] (a string list) idempotently,
// returning whether anything was added. Existing entries — including the user's
// unrelated ones — are preserved.
func mergePermList(perms map[string]interface{}, key string, adds []string) bool {
	list, _ := perms[key].([]interface{})
	have := map[string]bool{}
	for _, e := range list {
		if s, ok := e.(string); ok {
			have[s] = true
		}
	}
	changed := false
	for _, a := range adds {
		if !have[a] {
			list = append(list, a)
			have[a] = true
			changed = true
		}
	}
	if changed {
		perms[key] = list
	}
	return changed
}

// prunePermList removes an exact-match entry from perms[key], returning whether
// anything was removed. Used to drop stale pre-v2 permission patterns on upgrade.
func prunePermList(perms map[string]interface{}, key, remove string) bool {
	list, ok := perms[key].([]interface{})
	if !ok {
		return false
	}
	out := list[:0]
	removed := false
	for _, e := range list {
		if s, ok := e.(string); ok && s == remove {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if removed {
		perms[key] = out
	}
	return removed
}

// Stable markers wrap the managed flow-breaker section so re-running the install
// can REPLACE it in place (rather than skipping or duplicating), and so an old
// unmarked section can be migrated to the current text.
const (
	fbBeginMarker = "<!-- flow-breaker:begin -->"
	fbEndMarker   = "<!-- flow-breaker:end -->"
)

// claudeMDBlock is the marker-wrapped flow-breaker section, with no trailing
// newline (the caller normalizes EOF).
func claudeMDBlock() string {
	body := "## flow-breaker\n\n" +
		"A SessionStart hook runs `flow-breaker nudge` once (loud) at the start of each session. A UserPromptSubmit hook runs `flow-breaker nudge --quiet` on every turn: the daemon gates it, so it stays silent unless something is interrupt-worthy (an alarm firing, a task overdue, or the next task due within ~10 minutes). It fails open on an 800ms timeout, so a missing or slow daemon never blocks the turn.\n\n" +
		"When a nudge surfaces: **SURFACE it and continue — never act on it.** Mention the alert prominently to the user, then carry on with exactly what they asked. An overdue task must not make you abandon the user's work; the user decides what to do about it."
	return fbBeginMarker + "\n" + body + "\n" + fbEndMarker
}

// upsertFlowBreakerSection returns content with the managed flow-breaker block
// installed, plus an action tag. Precedence: (1) markers present → replace the
// block between them in place; (2) an old unmarked `## flow-breaker` heading →
// replace from that heading to the next top-level `##`/EOF (migration); (3) else
// append. The result is EOF-normalized to a single trailing newline; the caller
// treats an unchanged result as a no-op.
func upsertFlowBreakerSection(content, block string) (string, string) {
	normalize := func(s string) string { return strings.TrimRight(s, "\n") + "\n" }

	if begin := strings.Index(content, fbBeginMarker); begin != -1 {
		if end := strings.Index(content, fbEndMarker); end > begin {
			before := content[:begin]
			after := content[end+len(fbEndMarker):]
			return normalize(before + block + after), "replaced"
		}
	}

	hStart := -1
	if strings.HasPrefix(content, "## flow-breaker") {
		hStart = 0
	} else if i := strings.Index(content, "\n## flow-breaker"); i != -1 {
		hStart = i + 1
	}
	if hStart != -1 {
		before := content[:hStart]
		sectionEnd := len(content)
		if nl := strings.Index(content[hStart:], "\n"); nl != -1 {
			searchFrom := hStart + nl + 1
			if j := strings.Index(content[searchFrom:], "\n## "); j != -1 {
				sectionEnd = searchFrom + j
			}
		}
		return normalize(before + block + content[sectionEnd:]), "migrated"
	}

	if strings.TrimSpace(content) == "" {
		return normalize(block), "added"
	}
	return normalize(content + "\n\n" + block), "added"
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

	newContent, action := upsertFlowBreakerSection(content, claudeMDBlock())
	if newContent == content {
		fmt.Println("✓ CLAUDE.md already configured")
		return
	}

	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", path, err)
		os.Exit(1)
	}
	switch action {
	case "migrated":
		fmt.Println("✓ migrated flow-breaker section in ~/.claude/CLAUDE.md")
	case "replaced":
		fmt.Println("✓ updated flow-breaker section in ~/.claude/CLAUDE.md")
	default:
		fmt.Println("✓ added flow-breaker section to ~/.claude/CLAUDE.md")
	}
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
  flow-breaker list         list today's active tasks
  flow-breaker archive      list archived one-off tasks
  flow-breaker done <q>     mark a task done
  flow-breaker capture <text>  free-text capture (schedules or floats to inbox)
  flow-breaker snooze <id> [min]  snooze a task
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
  The CLI talks to the daemon over HTTP/JSON. Start it with:
    flow-breaker daemon            # serves 127.0.0.1:7777 (loopback)

  Base URL resolution (highest priority first):
    FLOW_BREAKER_URL env var
    ~/.config/flow-breaker/client  (file with a URL)
    http://localhost:7777          (default)

  # from Claude Code / any script:
  curl -s http://localhost:7777/status | jq .nudge
  curl -s http://localhost:7777/tasks
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
