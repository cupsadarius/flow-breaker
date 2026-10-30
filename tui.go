package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── TUI Model ──────────────────────────────────────────────────────────────

type inputField int

const (
	fieldNone inputField = iota
	fieldTime
	fieldDesc
	fieldRecurrence
	fieldDays
	fieldTags
	fieldConfirm
)

type model struct {
	store *Store // read-only display cache, refreshed from the daemon each tick
	// report holds the latest GET /status result (alarm firing / overdue / nudge).
	report         *StatusReport
	offline        bool      // true when the daemon is unreachable (fail loud, use cache)
	lastSync       time.Time // wall-clock time of the last successful daemon sync
	alertedID      int       // task ID of the alarm already alerted locally (0 = none)
	cursor         int
	width          int
	height         int
	msg            string
	msgExpiry      time.Time
	inputMode      bool
	inputField     inputField
	inputTime      string
	inputDesc      string
	inputRec       Recurrence
	inputTags      string
	inputDays      [7]bool
	dayCursor      int
	recCursor      int
	editingID      int
	confirmDel     bool
	settingsMode   bool
	settingsCursor int
	// panes
	focus       pane
	inbox       []Task
	habits      []Habit
	doneOpen    bool // DONE group expanded in the Today pane
	habitCursor int
	inboxCursor int
	bar         barMode
	barText     string
	// archive view
	archiveMode   bool
	archiveCursor int
	// feed management
	feedsMode       bool
	feedsCursor     int
	feedsList       []CalendarFeed
	feedsAddURL     bool
	feedsAddLabel   bool
	feedsInputURL   string
	feedsInputLabel string
	feedsConfirmDel bool
	feedsMsg        string
}

type tickMsg time.Time

// syncMsg carries the result of a daemon refresh (GET /tasks?scope=all + /status).
type syncMsg struct {
	tasks  []Task
	report *StatusReport
	inbox  []Task
	habits []Habit
	err    error
}

// settingsMsg carries the result of GET /settings (fetched once at init).
type settingsMsg struct {
	settings Settings
	err      error
}

// actionMsg carries the result of a mutation request (POST/DELETE). On success
// it triggers an immediate resync so the cache reflects the change.
type actionMsg struct {
	ok  string // transient message to show on success
	err error
}

func tickCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// syncCmd fetches the full task list and status report from the daemon.
func syncCmd() tea.Cmd {
	return func() tea.Msg {
		c := newClient()
		var tasks []Task
		if err := c.Get("/tasks?scope=all", &tasks); err != nil {
			return syncMsg{err: err}
		}
		var report StatusReport
		if err := c.Get("/status", &report); err != nil {
			return syncMsg{err: err}
		}
		var inbox []Task
		if err := c.Get("/inbox", &inbox); err != nil {
			return syncMsg{err: err}
		}
		var habits []Habit
		if err := c.Get("/habits", &habits); err != nil {
			return syncMsg{err: err}
		}
		return syncMsg{tasks: tasks, report: &report, inbox: inbox, habits: habits}
	}
}

// fetchSettingsCmd fetches settings from the daemon (source of truth).
func fetchSettingsCmd() tea.Cmd {
	return func() tea.Msg {
		var s Settings
		err := newClient().Get("/settings", &s)
		return settingsMsg{settings: s, err: err}
	}
}

// taskActionCmd performs a single mutation request and reports the outcome.
func taskActionCmd(method, path string, body any, okMsg string) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch method {
		case "POST":
			err = newClient().Post(path, body, nil)
		case "DELETE":
			err = newClient().Delete(path, body, nil)
		}
		return actionMsg{ok: okMsg, err: err}
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), syncCmd(), fetchSettingsCmd())
}

// ── Update ─────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		cmds := []tea.Cmd{tickCmd(), syncCmd()}
		select {
		case res := <-macAlertResult:
			cmds = append(cmds, m.dialogActionCmd(res))
		default:
		}
		return m, tea.Batch(cmds...)

	case syncMsg:
		return m, m.applySync(msg)

	case settingsMsg:
		if msg.err == nil {
			m.store.Settings = msg.settings
		}
		return m, nil

	case actionMsg:
		return m.handleActionResult(msg)

	case tea.KeyMsg:
		if m.confirmDel {
			return m.handleConfirmDel(msg)
		}
		if m.archiveMode {
			return m.handleArchive(msg)
		}
		if m.feedsMode {
			return m.handleFeeds(msg)
		}
		if m.settingsMode {
			return m.handleSettings(msg)
		}
		if m.inputMode {
			return m.handleInput(msg)
		}
		if m.bar != barOff {
			return m.handleBar(msg)
		}
		if m.report != nil && m.report.AlarmFiring {
			return m.handleAlarmKey(msg)
		}
		return m.handleNormal(msg)
	}
	return m, nil
}

// applySync folds a daemon refresh into the model. On success it replaces the
// display cache and clears the offline flag; on any error it fails loud (sets
// offline) while KEEPING the last-known cache — never a false empty/all-clear.
// Returns a command to run (a settings refetch when recovering from offline).
func (m *model) applySync(msg syncMsg) tea.Cmd {
	if msg.err != nil {
		m.offline = true
		return nil
	}
	wasOffline := m.offline
	m.offline = false
	m.lastSync = time.Now()
	m.store.Tasks = msg.tasks
	m.report = msg.report
	m.inbox = msg.inbox
	m.habits = msg.habits
	m.clampCursors()
	m.syncAlert()
	// Settings are fetched once at Init; if the daemon was down then (or the
	// settings fetch failed), refresh them now that we've reconnected.
	if wasOffline {
		return fetchSettingsCmd()
	}
	return nil
}

// Swapped in tests so syncAlert can run without osascript/say/rex side effects.
var (
	alertFn      = alertAll
	clearAlertFn = clearPaneAlert
)

// syncAlert raises local alerts (notification, sound, pane switch…) once per
// firing alarm, and clears the pane status when the alarm stops. The daemon
// owns firing; this only reacts to its edges as seen in /status.
func (m *model) syncAlert() {
	r := m.report
	if r != nil && r.AlarmFiring && r.AlarmTask != nil {
		if r.AlarmTask.ID != m.alertedID {
			m.alertedID = r.AlarmTask.ID
			alertFn("Flow Breaker", r.AlarmTask.Desc, &m.store.Settings)
		}
		return
	}
	if m.alertedID != 0 {
		m.alertedID = 0
		clearAlertFn()
	}
}

// dialogActionCmd maps a macAlert button to the matching daemon action on the
// alerted task.
func (m model) dialogActionCmd(button string) tea.Cmd {
	if m.alertedID == 0 {
		return nil
	}
	body := map[string]any{"source": "tui"}
	switch {
	case strings.Contains(button, "Snooze"):
		return taskActionCmd("POST", fmt.Sprintf("/task/%d/snooze", m.alertedID), body, "Snoozed")
	case strings.Contains(button, "Done"):
		return taskActionCmd("POST", fmt.Sprintf("/task/%d/done", m.alertedID), body, "Marked done")
	case strings.Contains(button, "Dismiss"):
		return taskActionCmd("POST", fmt.Sprintf("/task/%d/dismiss", m.alertedID), body, "Dismissed")
	}
	return nil
}

// clampCursors keeps list cursors within bounds after the cache is replaced, so
// an externally-shrunk list can't leave a highlight pointing past the end.
func (m *model) clampCursors() {
	m.cursor = moveCursor(m.cursor, len(m.todayRows()), "")
	m.habitCursor = moveCursor(m.habitCursor, len(m.habits), "")
	m.inboxCursor = moveCursor(m.inboxCursor, len(m.inbox), "")
	m.archiveCursor = moveCursor(m.archiveCursor, len(m.store.ArchivedTasks()), "")
}

// handleActionResult processes a mutation outcome: on error, fail loud; on
// success, show the message and immediately resync so the cache reflects it.
func (m model) handleActionResult(msg actionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if errors.Is(msg.err, ErrDaemonDown) {
			m.offline = true
			m.setMsg("daemon down")
		} else {
			m.setMsg(msg.err.Error())
		}
		return m, nil
	}
	if msg.ok != "" {
		m.setMsg(msg.ok)
	}
	return m, syncCmd()
}

func (m model) handleNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.focus = (m.focus + 1) % 3
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
		return m, nil
	case "a", "/":
		m.bar, m.barText = barAdd, ""
		return m, nil
	case "f":
		m.feedsMode = true
		m.feedsCursor = 0
		m.feedsList, _ = loadFeeds()
		return m, nil
	case "o":
		m.settingsMode = true
		m.settingsCursor = 0
		return m, nil
	case "v":
		if len(m.store.ArchivedTasks()) == 0 {
			m.setMsg("No archived tasks")
			return m, nil
		}
		m.archiveMode = true
		m.archiveCursor = 0
		return m, nil
	}
	switch m.focus {
	case paneToday:
		return m.handleToday(key)
	case paneHabits:
		return m.handleHabits(key)
	}
	return m.handleInbox(key)
}

// startInput opens the add/edit form prefilled from t; t.ID 0 means add.
func (m *model) startInput(t Task) {
	m.inputMode = true
	m.editingID = t.ID
	m.inputField = fieldTime
	m.inputTime = t.Time
	m.inputDesc = t.Desc
	m.inputRec = t.Recurrence
	m.inputTags = strings.Join(t.Tags, ", ")
	m.inputDays = boolsFromDays(t.Days)
	m.recCursor = 1 // Daily
	for i, opt := range recOptions {
		if opt == t.Recurrence {
			m.recCursor = i
		}
	}
}

// handleAlarmKey acks the firing alarm by its task ID via the daemon. The firing
// task comes from m.report (owned by the daemon scheduler), never local state.
func (m model) handleAlarmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.report == nil || !m.report.AlarmFiring || m.report.AlarmTask == nil {
		return m, nil
	}
	if m.offline {
		m.setMsg("daemon down")
		return m, nil
	}
	id := m.report.AlarmTask.ID
	body := map[string]any{"source": "tui"}
	switch msg.String() {
	case " ", "enter":
		return m, taskActionCmd("POST", fmt.Sprintf("/task/%d/dismiss", id), body, "Dismissed")
	case "s":
		return m, taskActionCmd("POST", fmt.Sprintf("/task/%d/snooze", id), body, "Snoozed")
	case "c":
		return m, taskActionCmd("POST", fmt.Sprintf("/task/%d/done", id), body, "Marked done")
	}
	return m, nil
}

func (m model) handleConfirmDel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		m.confirmDel = false
		if m.offline {
			m.setMsg("daemon down")
			return m, nil
		}
		if t, ok := m.selectedInbox(); ok {
			return m, taskActionCmd("DELETE", fmt.Sprintf("/inbox/%d", t.ID),
				map[string]any{"source": "tui"}, "Deleted from inbox")
		}
		if t, ok := m.selectedTask(); ok {
			return m, taskActionCmd("DELETE", fmt.Sprintf("/task/%d", t.ID),
				map[string]any{"source": "tui"}, "Deleted")
		}
	default:
		m.confirmDel = false
	}
	return m, nil
}

var (
	recOptions = []Recurrence{Once, Daily, Weekdays, Weekly, Monthly, Custom}
	dayLabels  = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	dayKeys    = [7]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
)

func needsDayPicker(rec Recurrence) bool {
	return rec == Weekdays || rec == Weekly || rec == Custom
}

func daysFromBools(bools [7]bool) []string {
	var days []string
	for i, on := range bools {
		if on {
			days = append(days, dayKeys[i])
		}
	}
	return days
}

func boolsFromDays(days []string) [7]bool {
	var b [7]bool
	for _, d := range days {
		for i, k := range dayKeys {
			if d == k {
				b[i] = true
			}
		}
	}
	return b
}

func (m model) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		m.inputMode = false
		return m, nil
	}

	switch m.inputField {
	case fieldTime:
		switch key {
		case "enter":
			if _, ok := parseTaskTime(m.inputTime); ok {
				m.inputField = fieldDesc
			} else {
				m.setMsg("Invalid time — use HH:MM")
			}
		case "backspace", "ctrl+h":
			if len(m.inputTime) > 0 {
				m.inputTime = m.inputTime[:len(m.inputTime)-1]
			}
		default:
			if len(key) == 1 {
				m.inputTime += key
			}
		}

	case fieldDesc:
		switch key {
		case "enter":
			if m.inputDesc != "" {
				m.inputField = fieldRecurrence
			}
		case "backspace", "ctrl+h":
			if len(m.inputDesc) > 0 {
				m.inputDesc = m.inputDesc[:len(m.inputDesc)-1]
			}
		default:
			if len(key) == 1 || key == " " {
				m.inputDesc += key
			}
		}

	case fieldRecurrence:
		switch key {
		case "j", "down", "tab":
			m.recCursor = (m.recCursor + 1) % len(recOptions)
		case "k", "up", "shift+tab":
			m.recCursor = (m.recCursor - 1 + len(recOptions)) % len(recOptions)
		case "enter", " ":
			m.inputRec = recOptions[m.recCursor]
			if needsDayPicker(m.inputRec) {
				m.dayCursor = 0
				// pre-fill weekdays for "weekdays" recurrence
				if m.inputRec == Weekdays {
					m.inputDays = [7]bool{true, true, true, true, true, false, false}
				}
				m.inputField = fieldDays
			} else {
				m.inputField = fieldTags
			}
		}

	case fieldDays:
		switch key {
		case "l", "right", "tab":
			m.dayCursor = (m.dayCursor + 1) % 7
		case "h", "left", "shift+tab":
			m.dayCursor = (m.dayCursor - 1 + 7) % 7
		case " ":
			m.inputDays[m.dayCursor] = !m.inputDays[m.dayCursor]
		case "enter":
			m.inputField = fieldTags
		}

	case fieldTags:
		switch key {
		case "enter":
			m.inputField = fieldConfirm
		case "backspace", "ctrl+h":
			if len(m.inputTags) > 0 {
				m.inputTags = m.inputTags[:len(m.inputTags)-1]
			}
		default:
			if len(key) == 1 || key == " " {
				m.inputTags += key
			}
		}

	case fieldConfirm:
		switch key {
		case "enter", "y":
			var tags []string
			if m.inputTags != "" {
				for _, t := range strings.Split(m.inputTags, ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						tags = append(tags, t)
					}
				}
			}
			days := daysFromBools(m.inputDays)
			editingID := m.editingID
			m.inputMode = false
			m.editingID = 0
			if m.offline {
				m.setMsg("daemon down")
				return m, nil
			}
			if editingID > 0 {
				// Send tags/days as explicit (non-nil) slices so a cleared field
				// overwrites rather than being treated as "unchanged" by the edit
				// route's pointer-omission semantics.
				if tags == nil {
					tags = []string{}
				}
				if days == nil {
					days = []string{}
				}
				body := map[string]any{
					"time":       m.inputTime,
					"desc":       m.inputDesc,
					"recurrence": string(m.inputRec),
					"tags":       tags,
					"days":       days,
					"source":     "tui",
				}
				return m, taskActionCmd("POST", fmt.Sprintf("/task/%d", editingID), body,
					fmt.Sprintf("Updated: %s %s", m.inputTime, m.inputDesc))
			}
			body := map[string]any{
				"time":       m.inputTime,
				"desc":       m.inputDesc,
				"recurrence": string(m.inputRec),
				"tags":       tags,
				"days":       days,
				"source":     "tui",
			}
			return m, taskActionCmd("POST", "/task", body,
				fmt.Sprintf("Added: %s %s", m.inputTime, m.inputDesc))
		case "n":
			m.inputMode = false
			m.editingID = 0
		}
	}
	return m, nil
}

func (m *model) setMsg(s string) {
	m.msg = s
	m.msgExpiry = time.Now().Add(3 * time.Second)
}

func (m *model) reloadFeeds() {
	m.feedsList, _ = loadFeeds()
	if m.feedsCursor >= len(m.feedsList) {
		m.feedsCursor = max(0, len(m.feedsList)-1)
	}
}

func (m model) handleFeeds(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// delete confirmation sub-mode
	if m.feedsConfirmDel {
		if key == "y" && m.feedsCursor >= 0 && m.feedsCursor < len(m.feedsList) {
			feed := m.feedsList[m.feedsCursor]
			_ = removeFeed(feed.URL)
			m.reloadFeeds()
			m.feedsMsg = "Removed: " + feed.Label
		}
		m.feedsConfirmDel = false
		return m, nil
	}

	// add-label sub-mode
	if m.feedsAddLabel {
		if tea.Key(msg).Paste {
			m.feedsInputLabel += string(msg.Runes)
			return m, nil
		}
		switch key {
		case "enter":
			feedURL := m.feedsInputURL
			// Resolve file paths to absolute
			if !strings.HasPrefix(feedURL, "http://") && !strings.HasPrefix(feedURL, "https://") {
				abs, err := filepath.Abs(strings.TrimPrefix(feedURL, "file://"))
				if err == nil {
					feedURL = abs
				}
			}
			err := addFeed(feedURL, m.feedsInputLabel)
			if err != nil {
				m.feedsMsg = "Error: " + err.Error()
			} else {
				m.feedsMsg = "Added feed"
			}
			m.reloadFeeds()
			m.feedsAddLabel = false
		case "esc":
			m.feedsAddLabel = false
		case "backspace", "ctrl+h":
			if len(m.feedsInputLabel) > 0 {
				m.feedsInputLabel = m.feedsInputLabel[:len(m.feedsInputLabel)-1]
			}
		default:
			if len(key) == 1 && key[0] >= 32 {
				m.feedsInputLabel += key
			}
		}
		return m, nil
	}

	// add-URL sub-mode
	if m.feedsAddURL {
		if tea.Key(msg).Paste {
			m.feedsInputURL += string(msg.Runes)
			return m, nil
		}
		switch key {
		case "enter":
			isHTTP := strings.HasPrefix(m.feedsInputURL, "http://") || strings.HasPrefix(m.feedsInputURL, "https://")
			isFile := !isHTTP && len(m.feedsInputURL) > 0
			if isHTTP || isFile {
				m.feedsAddURL = false
				m.feedsAddLabel = true
				m.feedsInputLabel = ""
			} else {
				m.feedsMsg = "Enter a URL or file path"
			}
		case "esc":
			m.feedsAddURL = false
		case "backspace", "ctrl+h":
			if len(m.feedsInputURL) > 0 {
				m.feedsInputURL = m.feedsInputURL[:len(m.feedsInputURL)-1]
			}
		default:
			if len(key) == 1 && key[0] >= 32 {
				m.feedsInputURL += key
			}
		}
		return m, nil
	}

	// list mode
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.feedsMode = false
	case "j", "down":
		if m.feedsCursor < len(m.feedsList)-1 {
			m.feedsCursor++
		}
	case "k", "up":
		if m.feedsCursor > 0 {
			m.feedsCursor--
		}
	case "a":
		m.feedsAddURL = true
		m.feedsInputURL = ""
		m.feedsInputLabel = ""
		m.feedsMsg = ""
	case "d", "x":
		if len(m.feedsList) > 0 {
			m.feedsConfirmDel = true
		}
	}
	return m, nil
}

func (m model) handleArchive(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	archived := m.store.ArchivedTasks()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "v":
		m.archiveMode = false
	case "j", "down":
		if m.archiveCursor < len(archived)-1 {
			m.archiveCursor++
		}
	case "k", "up":
		if m.archiveCursor > 0 {
			m.archiveCursor--
		}
	case "d", "x":
		if m.offline {
			m.setMsg("daemon down")
			return m, nil
		}
		if m.archiveCursor >= 0 && m.archiveCursor < len(archived) {
			id := archived[m.archiveCursor].ID
			if m.archiveCursor > 0 && m.archiveCursor >= len(archived)-1 {
				m.archiveCursor--
			}
			if len(archived) <= 1 {
				m.archiveMode = false
			}
			return m, taskActionCmd("DELETE", fmt.Sprintf("/task/%d", id),
				map[string]any{"source": "tui"}, "Deleted from archive")
		}
	}
	return m, nil
}

func (m model) renderArchive() string {
	archived := m.store.ArchivedTasks()
	var inner strings.Builder
	inner.WriteString(inputLabelStyle.Render("  Archive — past one-off tasks"))
	inner.WriteString("\n\n")

	if len(archived) == 0 {
		inner.WriteString(dimStyle.Render("  No archived tasks"))
		inner.WriteString("\n")
	} else {
		for i, t := range archived {
			icon := "·"
			if t.Done {
				icon = "✓"
			} else if t.Dismissed {
				icon = "–"
			}

			archivedDate := ""
			if t.ArchivedAt != "" {
				if parsed, err := time.Parse(time.RFC3339, t.ArchivedAt); err == nil {
					archivedDate = parsed.Format("Jan 02")
				}
			}

			tags := ""
			if len(t.Tags) > 0 {
				tags = " (" + strings.Join(t.Tags, ", ") + ")"
			}

			line := fmt.Sprintf("  %s %5s  %-30s%s  %s", icon, t.Time, t.Desc, tags, dimStyle.Render(archivedDate))

			if i == m.archiveCursor {
				inner.WriteString(cursorStyle.Render(fmt.Sprintf("  %s %5s  %-30s%s  ", icon, t.Time, t.Desc, tags)))
				inner.WriteString(dimStyle.Render(archivedDate))
			} else if t.Done {
				inner.WriteString(doneStyle.Render(line))
			} else {
				inner.WriteString(normalStyle.Render(line))
			}
			inner.WriteString("\n")
		}
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  j/k:navigate  d:delete  v/esc:back"))
	inner.WriteString("\n")

	boxWidth := m.width - 4
	if boxWidth < 60 {
		boxWidth = 60
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("14")).
		Padding(1, 2).
		Width(boxWidth)

	return box.Render(inner.String()) + "\n"
}

func (m model) renderFeeds() string {
	var inner strings.Builder
	inner.WriteString(inputLabelStyle.Render("  Calendar Feeds"))
	inner.WriteString("\n\n")

	if len(m.feedsList) == 0 && !m.feedsAddURL && !m.feedsAddLabel {
		inner.WriteString(dimStyle.Render("  No feeds — press 'a' to add one"))
		inner.WriteString("\n")
	} else {
		for i, f := range m.feedsList {
			label := f.Label
			if label == "" {
				label = "(no label)"
			}
			url := f.URL
			maxURL := 40
			if len(url) > maxURL {
				url = url[:maxURL-3] + "..."
			}
			line := fmt.Sprintf("  %-14s %s", label, url)
			if i == m.feedsCursor {
				inner.WriteString(inputActiveStyle.Render("  ▸ " + line[2:]))
			} else {
				inner.WriteString(normalStyle.Render("    " + line[2:]))
			}
			inner.WriteString("\n")
		}
	}

	if m.feedsAddURL {
		inner.WriteString("\n")
		inner.WriteString(inputActiveStyle.Render("  URL or path: " + m.feedsInputURL + "█"))
		inner.WriteString("\n")
	}

	if m.feedsAddLabel {
		inner.WriteString("\n")
		inner.WriteString(inputActiveStyle.Render("  Label (optional): " + m.feedsInputLabel + "█"))
		inner.WriteString("\n")
	}

	if m.feedsConfirmDel && m.feedsCursor >= 0 && m.feedsCursor < len(m.feedsList) {
		feed := m.feedsList[m.feedsCursor]
		label := feed.Label
		if label == "" {
			label = feed.URL
		}
		inner.WriteString("\n")
		inner.WriteString(urgentStyle.Render(fmt.Sprintf("  Delete %q? (y/n)", label)))
		inner.WriteString("\n")
	}

	if m.feedsMsg != "" {
		inner.WriteString("\n")
		inner.WriteString(dimStyle.Render("  " + m.feedsMsg))
		inner.WriteString("\n")
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  a:add  d:remove  esc:back"))
	inner.WriteString("\n")

	boxWidth := m.width - 4
	if boxWidth < 60 {
		boxWidth = 60
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("14")).
		Padding(1, 2).
		Width(boxWidth)

	return box.Render(inner.String()) + "\n"
}

// ── View ───────────────────────────────────────────────────────────────────

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("13")).
			Align(lipgloss.Center)

	nextStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("11")).
			PaddingLeft(1)

	urgentStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("9"))

	doneStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	cursorStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("0")).
			Foreground(lipgloss.Color("15"))

	alarmBoxA = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("9")).
			Foreground(lipgloss.Color("0")).
			Padding(0, 2)

	alarmBoxB = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("11")).
			Foreground(lipgloss.Color("0")).
			Padding(0, 2)

	inputLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	inputActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("11")).
				Bold(true)

	msgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Italic(true)

	habitDoneStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")).
			Bold(true)

	habitMissStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	habitDismissStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("11"))

	offlineStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("9")).
			Foreground(lipgloss.Color("0")).
			Padding(0, 2)
)

const (
	minWidth  = 60
	minHeight = 20
)

func (m model) View() string {
	var b strings.Builder
	w := max(m.width, minWidth)
	h := max(m.height, minHeight)

	title := headerStyle.Render("flow-breaker") + "  " + dimStyle.Render(time.Now().Format("Mon 2 Jan · 15:04"))
	dot := habitDoneStyle.Render("●") + dimStyle.Render(" daemon")
	if m.offline {
		dot = urgentStyle.Render("●") + dimStyle.Render(" daemon")
	}
	gap := max(1, w-lipgloss.Width(title)-lipgloss.Width(dot))
	b.WriteString(title + strings.Repeat(" ", gap) + dot + "\n")
	used := 1

	// offline banner — fail loud when the daemon is unreachable
	if m.offline {
		msg := "⚠ daemon unreachable — never synced"
		if !m.lastSync.IsZero() {
			msg = fmt.Sprintf("⚠ daemon unreachable — stale as of %s", m.lastSync.Format("15:04"))
		}
		b.WriteString(offlineStyle.Width(w).Render(msg) + "\n")
		used++
	}

	// alarm bar — firing state is owned by the daemon (m.report), not local state
	if m.report != nil && m.report.AlarmFiring && m.report.AlarmTask != nil {
		t := *m.report.AlarmTask
		flash := "🔔"
		style := alarmBoxA
		if (time.Now().UnixMilli()/500)%2 == 0 {
			flash = "🔕"
			style = alarmBoxB
		}
		bar := fmt.Sprintf(" %s  TIME TO: %s  ·  SPACE dismiss · S snooze · C done ",
			flash, strings.ToUpper(t.Desc))
		b.WriteString(style.Width(w).Render(bar) + "\n")
		used++
	}

	switch {
	case m.settingsMode:
		b.WriteString(m.renderSettings(w))
	case m.archiveMode:
		b.WriteString(m.renderArchive())
	case m.feedsMode:
		b.WriteString(m.renderFeeds())
	case m.inputMode:
		b.WriteString(m.renderInput())
	default:
		b.WriteString(m.renderPanes(w, h-used-3) + "\n")
	}

	if m.confirmDel {
		t, ok := m.selectedInbox()
		if !ok {
			t, ok = m.selectedTask()
		}
		if ok {
			b.WriteString(urgentStyle.Render(fmt.Sprintf("  Delete '%s'? (y/n)", t.Desc)) + "\n")
		}
	}
	if m.msg != "" && time.Now().Before(m.msgExpiry) {
		b.WriteString(msgStyle.Render("  "+m.msg) + "\n")
	}
	if !m.inputMode && !m.confirmDel && !m.archiveMode && !m.settingsMode && !m.feedsMode {
		b.WriteString(m.barLine() + "\n")
		b.WriteString(dimStyle.Render(" "+m.footerKeys()) + "\n")
	}

	return lipgloss.Place(w, h, lipgloss.Left, lipgloss.Top, b.String())
}

func (m model) renderInput() string {
	var inner strings.Builder
	title := "Add Task"
	if m.editingID > 0 {
		title = "Edit Task"
	}
	inner.WriteString(inputLabelStyle.Render("  " + title))
	inner.WriteString("\n\n")

	label := "  Time (HH:MM): "
	if m.inputField == fieldTime {
		inner.WriteString(inputActiveStyle.Render(label + m.inputTime + "█"))
	} else {
		inner.WriteString(inputLabelStyle.Render(label + m.inputTime))
	}
	inner.WriteString("\n")

	label = "  Description:  "
	if m.inputField == fieldDesc {
		inner.WriteString(inputActiveStyle.Render(label + m.inputDesc + "█"))
	} else if m.inputField > fieldDesc {
		inner.WriteString(inputLabelStyle.Render(label + m.inputDesc))
	} else {
		inner.WriteString(dimStyle.Render(label))
	}
	inner.WriteString("\n")

	inner.WriteString("\n")
	inner.WriteString(inputLabelStyle.Render("  Recurrence:"))
	inner.WriteString("\n")
	for i, opt := range recOptions {
		if m.inputField == fieldRecurrence && i == m.recCursor {
			inner.WriteString(inputActiveStyle.Render("    ▸ " + string(opt)))
		} else if m.inputRec == opt && m.inputField > fieldRecurrence {
			inner.WriteString(inputLabelStyle.Render("    ● " + string(opt)))
		} else {
			inner.WriteString(dimStyle.Render("      " + string(opt)))
		}
		inner.WriteString("\n")
	}

	// day picker (shown after recurrence for applicable types)
	if m.inputField == fieldDays || (m.inputField > fieldDays && needsDayPicker(m.inputRec)) {
		inner.WriteString("\n")
		inner.WriteString(inputLabelStyle.Render("  Days:"))
		inner.WriteString("\n    ")
		for i, lbl := range dayLabels {
			check := " "
			if m.inputDays[i] {
				check = "x"
			}
			item := fmt.Sprintf("[%s] %s", check, lbl)
			if m.inputField == fieldDays && i == m.dayCursor {
				inner.WriteString(inputActiveStyle.Render(item))
			} else if m.inputDays[i] {
				inner.WriteString(inputLabelStyle.Render(item))
			} else {
				inner.WriteString(dimStyle.Render(item))
			}
			if i < 6 {
				inner.WriteString("  ")
			}
		}
		inner.WriteString("\n")
	}

	inner.WriteString("\n")
	label = "  Tags (comma-sep): "
	if m.inputField == fieldTags {
		inner.WriteString(inputActiveStyle.Render(label + m.inputTags + "█"))
	} else if m.inputField > fieldTags {
		inner.WriteString(inputLabelStyle.Render(label + m.inputTags))
	} else {
		inner.WriteString(dimStyle.Render(label))
	}
	inner.WriteString("\n")

	if m.inputField == fieldConfirm {
		inner.WriteString("\n")
		confirmLabel := "  Confirm? (enter/y) or (esc/n)"
		if m.editingID > 0 {
			confirmLabel = "  Save changes? (enter/y) or (esc/n)"
		}
		inner.WriteString(inputActiveStyle.Render(confirmLabel))
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  esc to cancel"))
	inner.WriteString("\n")

	boxWidth := m.width - 4
	if boxWidth < 60 {
		boxWidth = 60
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("14")).
		Padding(1, 2).
		Width(boxWidth)

	return box.Render(inner.String()) + "\n"
}

var settingsLabels = [10]string{
	"Snooze duration",
	"Notification",
	"Modal dialog",
	"Text-to-speech",
	"System sound",
	"Terminal bell",
	"Pane switch (tmux/Rex)",
	"─── Calendar ───",
	"Calendar feeds",
	"Cache duration (min)",
}

// handleSettings drives the read-only settings screen. Editing settings moves to
// the daemon in Phase 6, so toggles/adjustments are disabled here; only
// navigation, opening (local) feed management, and closing remain.
func (m model) handleSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "o":
		m.settingsMode = false
	case "j", "down":
		if m.settingsCursor < 9 {
			m.settingsCursor++
			// skip separator row
			if m.settingsCursor == 7 {
				m.settingsCursor = 8
			}
		}
	case "k", "up":
		if m.settingsCursor > 0 {
			m.settingsCursor--
			// skip separator row
			if m.settingsCursor == 7 {
				m.settingsCursor = 6
			}
		}
	case "enter":
		// Calendar feeds are stored in local files (not the daemon), so managing
		// them stays available in this phase.
		if m.settingsCursor == 8 {
			m.feedsMode = true
			m.feedsCursor = 0
			m.feedsList, _ = loadFeeds()
			m.settingsMode = false
		}
	}
	return m, nil
}

func (m model) renderSettings(w int) string {
	var inner strings.Builder
	inner.WriteString(inputLabelStyle.Render("  Settings"))
	inner.WriteString("\n\n")

	s := &m.store.Settings

	for i, label := range settingsLabels {
		// separator row
		if i == 7 {
			inner.WriteString("\n")
			inner.WriteString(dimStyle.Render("  " + label))
			inner.WriteString("\n")
			continue
		}

		var val string
		switch i {
		case 0:
			val = fmt.Sprintf("%d min    [◄ ►]", s.SnoozeMins)
		case 1:
			val = boolVal(s.AlertNotify)
		case 2:
			val = boolVal(s.AlertDialog)
		case 3:
			val = boolVal(s.AlertSpeech)
		case 4:
			val = boolVal(s.AlertSound)
		case 5:
			val = boolVal(s.AlertBell)
		case 6:
			val = boolVal(s.AlertTmux)
		case 8:
			val = boolVal(s.CalEnabled)
			feeds, _ := loadFeeds()
			if len(feeds) == 0 {
				val += dimStyle.Render("  (no feeds — press enter to manage)")
			} else {
				val += dimStyle.Render(fmt.Sprintf("  (%d feed(s))", len(feeds)))
			}
		case 9:
			val = fmt.Sprintf("%d min    [◄ ►]", s.CalCacheMins)
		}

		line := fmt.Sprintf("  %-22s %s", label, val)
		if i == m.settingsCursor {
			inner.WriteString(inputActiveStyle.Render(line))
		} else {
			inner.WriteString(normalStyle.Render(line))
		}
		inner.WriteString("\n")
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  settings editing moves to the daemon (Phase 6)"))
	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  j/k:navigate  enter:manage feeds  esc:back"))
	inner.WriteString("\n")

	boxWidth := w - 4
	if boxWidth < 60 {
		boxWidth = 60
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("14")).
		Padding(1, 2).
		Width(boxWidth)

	return box.Render(inner.String()) + "\n"
}

func boolVal(b bool) string {
	if b {
		return habitDoneStyle.Render("ON")
	}
	return dimStyle.Render("OFF")
}
