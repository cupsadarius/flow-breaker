package main

import (
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
	store          *Store
	cursor         int
	alarm          *alarmState
	fired          map[int]bool
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
	scrollOffset   int
	habitView      bool
	habitFull      bool
	confirmDel     bool
	settingsMode   bool
	settingsCursor int
	// calendar integration
	calEvents       []CalendarEvent
	calSelected     []bool
	calCursor       int
	calSuggestMode  bool
	calTimelineMode bool
	calLoading      bool
	calError        string
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
type macAlertMsg string
type calEventsMsg struct {
	events []CalendarEvent
	err    error
}

func fetchCalEventsCmd(cacheMins int) tea.Cmd {
	return func() tea.Msg {
		feeds, err := loadFeeds()
		if err != nil {
			return calEventsMsg{err: err}
		}
		events, err := getCachedOrFetchEvents(feeds, cacheMins)
		return calEventsMsg{events: events, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func pollMacAlert() tea.Cmd {
	return func() tea.Msg {
		select {
		case result := <-macAlertResult:
			return macAlertMsg(result)
		case <-time.After(600 * time.Millisecond):
			return nil
		}
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd(), pollMacAlert()}
	if m.store.Settings.CalEnabled {
		m.calLoading = true
		cmds = append(cmds, fetchCalEventsCmd(m.store.Settings.CalCacheMins))
	}
	return tea.Batch(cmds...)
}

// ── Update ─────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		m.checkAlarms()
		m.checkDailyResetForCal()
		writeStatusFile(m.store, m.alarm)
		return m, tea.Batch(tickCmd(), pollMacAlert())

	case macAlertMsg:
		m.handleMacDialogResult(string(msg))
		return m, pollMacAlert()

	case calEventsMsg:
		m.calLoading = false
		if msg.err != nil {
			m.calError = msg.err.Error()
		} else {
			m.calEvents = msg.events
			m.calSelected = make([]bool, len(msg.events))
			m.calError = ""
		}
		return m, nil

	case tea.KeyMsg:
		if m.confirmDel {
			return m.handleConfirmDel(msg)
		}
		if m.archiveMode {
			return m.handleArchive(msg)
		}
		if m.calSuggestMode {
			return m.handleCalSuggestions(msg)
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
		if m.alarm.active {
			return m.handleAlarmKey(msg)
		}
		return m.handleNormal(msg)
	}
	return m, nil
}

func (m *model) handleMacDialogResult(result string) {
	idx := m.alarm.taskIdx
	if idx < 0 || idx >= len(m.store.Tasks) {
		return
	}
	switch {
	case strings.Contains(result, "Snooze"):
		snz := m.store.Settings.SnoozeMins
		m.store.Tasks[idx].Snoozed = time.Now().Add(time.Duration(snz) * time.Minute).Unix()
		delete(m.fired, m.store.Tasks[idx].ID)
		m.store.save()
		m.alarm.snooze(snz)
		m.setMsg(fmt.Sprintf("Snoozed %d min (via dialog)", snz))
	case strings.Contains(result, "Done"):
		m.store.Tasks[idx].Done = true
		m.store.Tasks[idx].Dismissed = true
		m.store.save()
		m.alarm.dismiss()
		m.setMsg("Marked done (via dialog)")
	case strings.Contains(result, "Dismiss"), strings.Contains(result, "gave up"):
		m.store.Tasks[idx].Dismissed = true
		m.store.save()
		m.alarm.dismiss()
		m.setMsg("Dismissed (via dialog)")
	}
}

func (m model) handleNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	active := m.store.ActiveTasks()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(active)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g":
		m.cursor = 0
	case "G":
		if len(active) > 0 {
			m.cursor = len(active) - 1
		}
	case "a":
		m.inputMode = true
		m.editingID = 0
		m.inputField = fieldTime
		m.inputTime = ""
		m.inputDesc = ""
		m.inputRec = Daily
		m.inputTags = ""
		m.inputDays = [7]bool{}
		m.recCursor = 1
	case "e":
		if m.cursor >= 0 && m.cursor < len(active) {
			t := active[m.cursor]
			m.inputMode = true
			m.editingID = t.ID
			m.inputField = fieldTime
			m.inputTime = t.Time
			m.inputDesc = t.Desc
			m.inputRec = t.Recurrence
			m.inputTags = strings.Join(t.Tags, ", ")
			m.inputDays = boolsFromDays(t.Days)
			// set recCursor to match current recurrence
			m.recCursor = 1 // default to Daily
			for i, opt := range recOptions {
				if opt == t.Recurrence {
					m.recCursor = i
					break
				}
			}
		}
	case "d", "x":
		if len(active) > 0 {
			m.confirmDel = true
		}
	case "c":
		realIdx := m.store.activeIndex(m.cursor)
		if realIdx >= 0 {
			t := &m.store.Tasks[realIdx]
			t.Done = !t.Done
			if t.Done {
				today := time.Now().Format("2006-01-02")
				if t.History == nil {
					t.History = make(map[string]bool)
				}
				t.History[today] = true
			}
			m.store.save()
		}
	case "h":
		m.habitView = !m.habitView
		m.habitFull = false
	case "f":
		if m.habitView {
			m.habitFull = !m.habitFull
		} else {
			m.feedsMode = true
			m.feedsCursor = 0
			m.feedsList, _ = loadFeeds()
		}
	case "p":
		if m.store.Settings.CalEnabled {
			m.calSuggestMode = true
			m.calCursor = 0
			m.calTimelineMode = false
			if len(m.calEvents) == 0 {
				m.calLoading = true
				return m, fetchCalEventsCmd(m.store.Settings.CalCacheMins)
			}
		} else {
			m.setMsg("Calendar disabled — enable in settings (o) and add feeds")
		}
	case "o":
		m.settingsMode = true
		m.settingsCursor = 0
	case "r":
		*m.store = loadStore()
		m.store.resetDaily()
		m.setMsg("Reloaded")
	case "v":
		archived := m.store.ArchivedTasks()
		if len(archived) > 0 {
			m.archiveMode = true
			m.archiveCursor = 0
		} else {
			m.setMsg("No archived tasks")
		}
	}
	return m, nil
}

func (m model) handleAlarmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	idx := m.alarm.taskIdx
	if idx < 0 || idx >= len(m.store.Tasks) {
		m.alarm.dismiss()
		return m, nil
	}
	switch msg.String() {
	case " ", "enter":
		m.store.Tasks[idx].Dismissed = true
		m.store.save()
		m.alarm.dismiss()
		m.setMsg("Dismissed")
	case "s":
		snz := m.store.Settings.SnoozeMins
		m.store.Tasks[idx].Snoozed = time.Now().Add(time.Duration(snz) * time.Minute).Unix()
		delete(m.fired, m.store.Tasks[idx].ID)
		m.store.save()
		m.alarm.snooze(snz)
		m.setMsg(fmt.Sprintf("Snoozed %d min", snz))
	case "c":
		m.store.Tasks[idx].Done = true
		m.store.Tasks[idx].Dismissed = true
		m.store.save()
		m.alarm.dismiss()
		m.setMsg("Marked done")
	}
	return m, nil
}

func (m model) handleConfirmDel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		realIdx := m.store.activeIndex(m.cursor)
		if realIdx >= 0 {
			m.store.deleteTask(m.store.Tasks[realIdx].ID)
			active := m.store.ActiveTasks()
			if m.cursor >= len(active) && m.cursor > 0 {
				m.cursor--
			}
			m.setMsg("Deleted")
		}
		m.confirmDel = false
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
				// quick-fill from calendar events
				if key >= "a" && key <= "i" && len(m.calEvents) > 0 && m.inputTime == "" {
					idx := int(key[0] - 'a')
					count := 0
					for _, ev := range m.calEvents {
						if ev.AllDay {
							continue
						}
						if count == idx {
							m.inputTime = ev.StartTime
							m.inputDesc = ev.Summary
							m.inputField = fieldRecurrence
							break
						}
						count++
					}
				} else {
					m.inputTime += key
				}
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
			if m.editingID > 0 {
				m.store.editTask(m.editingID, m.inputTime, m.inputDesc, m.inputRec, tags, days)
				m.inputMode = false
				m.editingID = 0
				m.setMsg(fmt.Sprintf("Updated: %s %s", m.inputTime, m.inputDesc))
			} else {
				m.store.addTask(m.inputTime, m.inputDesc, m.inputRec, tags, days)
				m.inputMode = false
				m.setMsg(fmt.Sprintf("Added: %s %s", m.inputTime, m.inputDesc))
			}
		case "n":
			m.inputMode = false
			m.editingID = 0
		}
	}
	return m, nil
}

func (m *model) checkOverdueOnStartup() {
	for i, t := range m.store.Tasks {
		if t.Done || t.Dismissed || m.fired[t.ID] {
			continue
		}
		if t.Snoozed > 0 && time.Now().Unix() < t.Snoozed {
			continue
		}
		if !shouldFireToday(t) {
			continue
		}
		if timeUntil(t) < 0 {
			m.alarm.trigger(i)
			m.fired[t.ID] = true
			alertAll("Flow Breaker", t.Desc, &m.store.Settings)
			break
		}
	}
}

func (m *model) checkAlarms() {
	if m.alarm.active {
		m.alarm.tick++
		if m.alarm.tick%10 == 0 {
			fmt.Print("\a")
			macSound("Ping")
		}
		return
	}
	now := time.Now()
	for i, t := range m.store.Tasks {
		if t.Done || t.Dismissed || m.fired[t.ID] {
			continue
		}
		if t.Snoozed > 0 && now.Unix() < t.Snoozed {
			continue
		}
		if !shouldFireToday(t) {
			continue
		}
		d := timeUntil(t)
		if d > 0 || d < -2*time.Minute {
			continue
		}
		m.alarm.trigger(i)
		m.fired[t.ID] = true
		alertAll("Flow Breaker", t.Desc, &m.store.Settings)
		break
	}
}

func (m *model) setMsg(s string) {
	m.msg = s
	m.msgExpiry = time.Now().Add(3 * time.Second)
}

// checkDailyResetForCal auto-shows calendar suggestions after daily reset
func (m *model) checkDailyResetForCal() {
	today := time.Now().Format("2006-01-02")
	if m.store.LastReset != today {
		m.store.resetDaily()
		if m.store.Settings.CalEnabled && !m.calLoading {
			m.calEvents = nil
			m.calLoading = true
		}
	}
}

func (m model) handleCalSuggestions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.calSuggestMode = false
		m.calTimelineMode = false
	case "j", "down":
		if m.calCursor < len(m.calEvents)-1 {
			m.calCursor++
		}
	case "k", "up":
		if m.calCursor > 0 {
			m.calCursor--
		}
	case " ":
		if m.calCursor >= 0 && m.calCursor < len(m.calSelected) {
			m.calSelected[m.calCursor] = !m.calSelected[m.calCursor]
		}
	case "a":
		allOn := true
		for _, s := range m.calSelected {
			if !s {
				allOn = false
				break
			}
		}
		for i := range m.calSelected {
			m.calSelected[i] = !allOn
		}
	case "enter":
		var selected []CalendarEvent
		for i, ev := range m.calEvents {
			if m.calSelected[i] {
				selected = append(selected, ev)
			}
		}
		if len(selected) > 0 {
			imported := m.store.importCalendarEvents(selected)
			m.setMsg(fmt.Sprintf("Imported %d event(s)", len(imported)))
		}
		m.calSuggestMode = false
		m.calTimelineMode = false
	case "r":
		m.calLoading = true
		m.calEvents = nil
		m.calSuggestMode = false
		return m, fetchCalEventsCmd(0) // bypass cache
	case "t":
		m.calTimelineMode = !m.calTimelineMode
	case "f":
		m.feedsMode = true
		m.feedsCursor = 0
		m.feedsList, _ = loadFeeds()
		m.calSuggestMode = false
	}
	return m, nil
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
		if m.archiveCursor >= 0 && m.archiveCursor < len(archived) {
			m.store.deleteTask(archived[m.archiveCursor].ID)
			archived = m.store.ArchivedTasks()
			if m.archiveCursor >= len(archived) && m.archiveCursor > 0 {
				m.archiveCursor--
			}
			if len(archived) == 0 {
				m.archiveMode = false
			}
			m.setMsg("Deleted from archive")
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

func (m model) isEventImported(ev CalendarEvent) bool {
	for _, t := range m.store.Tasks {
		if !t.Archived && t.Time == ev.StartTime && t.Desc == ev.Summary && hasTag(t.Tags, "gcal") {
			return true
		}
	}
	return false
}

// ── View ───────────────────────────────────────────────────────────────────

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("13")).
			Align(lipgloss.Center)

	clockStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("14"))

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
)

const (
	minWidth  = 60
	minHeight = 20
)

const banner = `
  ╔═══════════════════════════════════════╗
  ║    ⚡  F L O W — B R E A K E R  ⚡    ║
  ╚═══════════════════════════════════════╝`

func (m model) View() string {
	var b strings.Builder
	w := m.width
	if w < minWidth {
		w = minWidth
	}
	h := m.height
	if h < minHeight {
		h = minHeight
	}

	b.WriteString(headerStyle.Width(w).Render(banner))
	b.WriteString("\n")

	now := time.Now()
	timeFmt := now.Format("15:04:05")
	dateFmt := now.Format("Monday, January 2, 2006")
	clock := fmt.Sprintf("  %s  ·  %s", timeFmt, dateFmt)
	b.WriteString(clockStyle.Width(w).Align(lipgloss.Center).Render(clock))
	b.WriteString("\n\n")

	// alarm bar
	if m.alarm.active && m.alarm.taskIdx >= 0 && m.alarm.taskIdx < len(m.store.Tasks) {
		t := m.store.Tasks[m.alarm.taskIdx]
		flash := "🔔"
		style := alarmBoxA
		if m.alarm.tick%4 < 2 {
			flash = "🔕"
			style = alarmBoxB
		}
		bar := fmt.Sprintf(" %s  TIME TO: %s  ·  SPACE dismiss · S snooze · C done ",
			flash, strings.ToUpper(t.Desc))
		b.WriteString(style.Width(w).Render(bar))
		b.WriteString("\n\n")
	}

	// next up
	active := m.store.ActiveTasks()
	for _, t := range active {
		if t.Done || t.Dismissed || !shouldFireToday(t) {
			continue
		}
		d := timeUntil(t)
		if d < 0 {
			continue
		}
		style := nextStyle
		prefix := "  NEXT ▸ "
		if d < 5*time.Minute {
			style = urgentStyle
			prefix = "  ⚠ SOON ▸ "
		}
		line := fmt.Sprintf("%s%s  %s  (%s)", prefix, t.Time, t.Desc, fmtDuration(d))
		b.WriteString(style.Render(line))
		b.WriteString("\n\n")
		break
	}

	if m.settingsMode {
		b.WriteString(m.renderSettings(w))
	} else if m.habitView {
		b.WriteString(m.renderHabits(w, h))
	} else {
		// responsive column widths — compute dynamic STATUS width
		statusWidth := 11 // minimum ("overdue ..." baseline)
		for _, t := range active {
			var s string
			if t.Done {
				s = "done"
			} else if t.Dismissed {
				s = "dismissed"
			} else if !shouldFireToday(t) {
				s = nextOccurrenceLabel(t)
			} else if d := timeUntil(t); d < 0 {
				s = "overdue " + fmtDuration(d)
			} else {
				s = fmtDuration(timeUntil(t))
			}
			if len(s) > statusWidth {
				statusWidth = len(s)
			}
		}
		fixedWidth := 8 + 12 + 10 + (statusWidth + 2) + 22 + 3 // time + seps + repeat + status + habits + pad
		descWidth := w - fixedWidth
		if descWidth < 20 {
			descWidth = 20
		}

		// header with lipgloss border style — STATUS before dots
		hdr := fmt.Sprintf("  %-7s │ %-*s │ %-8s │ %-*s │ %s",
			"TIME", descWidth, "TASK", "REPEAT", statusWidth, "STATUS", "Mo Tu We Th Fr Sa Su")
		sep := fmt.Sprintf("  %s┼%s┼%s┼%s┼%s",
			strings.Repeat("─", 8),
			strings.Repeat("─", descWidth+2),
			strings.Repeat("─", 10),
			strings.Repeat("─", statusWidth+2),
			strings.Repeat("─", 22))
		b.WriteString(dimStyle.Render(hdr))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(sep))
		b.WriteString("\n")

		if len(active) == 0 {
			b.WriteString(dimStyle.Render("  No tasks yet — press 'a' to add one"))
			b.WriteString("\n")
		}

		// scrollable task list
		headerLines := 10 // banner + clock + next + table header
		footerLines := 5  // help + msg + padding
		visibleRows := h - headerLines - footerLines
		if visibleRows < 5 {
			visibleRows = 5
		}

		// adjust scroll offset to keep cursor visible
		if m.cursor < m.scrollOffset {
			m.scrollOffset = m.cursor
		}
		if m.cursor >= m.scrollOffset+visibleRows {
			m.scrollOffset = m.cursor - visibleRows + 1
		}

		taskCount := len(active)
		startIdx := m.scrollOffset
		endIdx := startIdx + visibleRows
		if endIdx > taskCount {
			endIdx = taskCount
		}

		// scroll indicator top
		if startIdx > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ▲ %d more above", startIdx)))
			b.WriteString("\n")
		}

		for i := startIdx; i < endIdx; i++ {
			t := active[i]
			icon := "·"
			status := fmtDuration(timeUntil(t))
			style := normalStyle

			if t.Done {
				icon = "✓"
				status = "done"
				style = doneStyle
			} else if t.Dismissed {
				icon = "–"
				status = "dismissed"
				style = dimStyle
			} else if !shouldFireToday(t) {
				status = nextOccurrenceLabel(t)
				style = dimStyle
			} else {
				d := timeUntil(t)
				if d < 0 {
					icon = "!"
					style = urgentStyle
					status = "overdue " + fmtDuration(d)
				} else if d < 5*time.Minute {
					icon = "▸"
					style = urgentStyle
				}
			}

			desc := t.Desc
			if len(t.Tags) > 0 {
				desc += " (" + strings.Join(t.Tags, ", ") + ")"
			}
			maxDesc := descWidth - 2 // account for icon + space
			if len(desc) > maxDesc {
				desc = desc[:maxDesc-3] + "..."
			}
			rec := string(t.Recurrence)

			// inline habit dots (Mon-Sun of current week)
			var dots string
			if t.Recurrence != Once {
				monday := weekStart(now)
				for d := 0; d < 7; d++ {
					day := monday.AddDate(0, 0, d)
					key := day.Format("2006-01-02")
					if d > 0 {
						dots += " "
					}
					today := day.Format("2006-01-02") == now.Format("2006-01-02")
					if t.History[key] || (today && t.Done) {
						dots += habitDoneStyle.Render("██")
					} else if day.After(now) {
						dots += "  "
					} else if today && t.Dismissed {
						dots += habitDismissStyle.Render("··")
					} else if !shouldFireOnDay(t, day.Weekday()) {
						dots += "  "
					} else {
						dots += habitMissStyle.Render("··")
					}
				}
			} else {
				dots = strings.Repeat("   ", 6) + "  " // 20 chars blank
			}

			// Main line: only plain text — safe to wrap in style.Render()
			mainLine := fmt.Sprintf("  %5s   │ %s %-*s │ %-8s │ %-*s │ ",
				t.Time, icon, maxDesc, desc, rec, statusWidth, status)

			if i == m.cursor {
				b.WriteString(cursorStyle.Render(mainLine))
			} else {
				b.WriteString(style.Render(mainLine))
			}
			// Dots and tags already styled — write directly, never wrapped
			b.WriteString(dots)
			b.WriteString("\n")
		}

		// scroll indicator bottom
		remaining := taskCount - endIdx
		if remaining > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ▼ %d more below", remaining)))
			b.WriteString("\n")
		}

		b.WriteString("\n")
	}

	if m.calSuggestMode {
		b.WriteString(m.renderCalSuggestions())
	}

	if m.feedsMode {
		b.WriteString(m.renderFeeds())
	}

	if m.inputMode {
		b.WriteString(m.renderInput())
	}

	if m.archiveMode {
		b.WriteString(m.renderArchive())
	}

	if m.confirmDel && m.cursor >= 0 && m.cursor < len(active) {
		t := active[m.cursor]
		b.WriteString(urgentStyle.Render(fmt.Sprintf("  Delete '%s'? (y/n)", t.Desc)))
		b.WriteString("\n")
	}

	if m.msg != "" && time.Now().Before(m.msgExpiry) {
		b.WriteString(msgStyle.Render("  " + m.msg))
		b.WriteString("\n")
	}

	if !m.inputMode && !m.confirmDel && !m.calSuggestMode && !m.archiveMode {
		b.WriteString("\n")
		helpKeys := "  a:add  e:edit  d:del  c:done  h:habits  v:archive  f:feeds"
		if m.store.Settings.CalEnabled {
			helpKeys += "  p:calendar"
		}
		helpKeys += "  o:settings  r:reload  j/k:nav  q:quit"
		b.WriteString(dimStyle.Render(helpKeys))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("  sock: %s  file: %s", sockPath(), statusPath())))
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

	// calendar hints when on time field
	if m.inputField == fieldTime && len(m.calEvents) > 0 {
		inner.WriteString("\n")
		inner.WriteString(dimStyle.Render("  Calendar:"))
		inner.WriteString("\n")
		count := 0
		for _, ev := range m.calEvents {
			if ev.AllDay || count >= 9 {
				break
			}
			count++
			inner.WriteString(dimStyle.Render(fmt.Sprintf("    %c) %s  %s", 'a'+count-1, ev.StartTime, ev.Summary)))
			inner.WriteString("\n")
		}
		inner.WriteString(dimStyle.Render("  (a-i to quick-fill, or type manually)"))
		inner.WriteString("\n")
	}

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
	"Tmux flash",
	"─── Calendar ───",
	"Calendar feeds",
	"Cache duration (min)",
}

func (m model) handleSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "o":
		m.settingsMode = false
		m.store.save()
		m.setMsg("Settings saved")
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
		if m.settingsCursor == 8 {
			m.feedsMode = true
			m.feedsCursor = 0
			m.feedsList, _ = loadFeeds()
			m.settingsMode = false
		} else {
			m.toggleSettingBool()
		}
	case " ":
		m.toggleSettingBool()
	case "h", "left":
		if m.settingsCursor == 0 && m.store.Settings.SnoozeMins > 1 {
			m.store.Settings.SnoozeMins--
		}
		if m.settingsCursor == 9 && m.store.Settings.CalCacheMins > 1 {
			m.store.Settings.CalCacheMins--
		}
	case "l", "right":
		if m.settingsCursor == 0 && m.store.Settings.SnoozeMins < 60 {
			m.store.Settings.SnoozeMins++
		}
		if m.settingsCursor == 9 && m.store.Settings.CalCacheMins < 120 {
			m.store.Settings.CalCacheMins++
		}
	}
	return m, nil
}

func (m *model) toggleSettingBool() {
	s := &m.store.Settings
	switch m.settingsCursor {
	case 1:
		s.AlertNotify = !s.AlertNotify
	case 2:
		s.AlertDialog = !s.AlertDialog
	case 3:
		s.AlertSpeech = !s.AlertSpeech
	case 4:
		s.AlertSound = !s.AlertSound
	case 5:
		s.AlertBell = !s.AlertBell
	case 6:
		s.AlertTmux = !s.AlertTmux
	case 8:
		s.CalEnabled = !s.CalEnabled
	}
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
	inner.WriteString(dimStyle.Render("  j/k:navigate  space:toggle  ◄►:adjust  esc:back"))
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

func weekStart(t time.Time) time.Time {
	wd := t.Weekday()
	if wd == time.Sunday {
		wd = 7
	}
	d := t.AddDate(0, 0, -int(wd-time.Monday))
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
}

var calBoxStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("14"))

func (m model) renderCalSuggestions() string {
	if m.calTimelineMode {
		return m.renderTimeline()
	}

	var inner strings.Builder
	now := time.Now()
	inner.WriteString(calBoxStyle.Render("  Calendar — " + now.Format("Monday, January 2")))
	inner.WriteString("\n\n")

	if m.calLoading {
		inner.WriteString(dimStyle.Render("  Loading calendar events..."))
		inner.WriteString("\n")
	} else if m.calError != "" {
		inner.WriteString(urgentStyle.Render("  Error: " + m.calError))
		inner.WriteString("\n")
	} else if len(m.calEvents) == 0 {
		inner.WriteString(dimStyle.Render("  No events today"))
		inner.WriteString("\n")
	} else {
		// timed events
		hasAllDay := false
		for i, ev := range m.calEvents {
			if ev.AllDay {
				hasAllDay = true
				continue
			}
			check := "[ ]"
			if m.calSelected[i] {
				check = "[x]"
			}
			imported := m.isEventImported(ev)

			line := fmt.Sprintf("  %s %s─%s  %s", check, ev.StartTime, ev.EndTime, ev.Summary)
			if imported {
				line += "  imported"
			}

			if i == m.calCursor {
				inner.WriteString(inputActiveStyle.Render(line))
			} else if imported {
				inner.WriteString(dimStyle.Render(line))
			} else {
				inner.WriteString(normalStyle.Render(line))
			}
			inner.WriteString("\n")
		}

		// all-day section
		if hasAllDay {
			inner.WriteString("\n")
			inner.WriteString(dimStyle.Render("  ── All-day ──"))
			inner.WriteString("\n")
			for i, ev := range m.calEvents {
				if !ev.AllDay {
					continue
				}
				check := "[ ]"
				if m.calSelected[i] {
					check = "[x]"
				}
				line := fmt.Sprintf("  %s %s", check, ev.Summary)
				if i == m.calCursor {
					inner.WriteString(inputActiveStyle.Render(line))
				} else {
					inner.WriteString(normalStyle.Render(line))
				}
				inner.WriteString("\n")
			}
		}
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  SPACE:toggle  ENTER:import selected  a:all  r:refresh"))
	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  t:timeline  f:feeds  esc:dismiss"))
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

func (m model) renderTimeline() string {
	var inner strings.Builder
	now := time.Now()
	inner.WriteString(calBoxStyle.Render("  Timeline — " + now.Format("Monday, January 2")))
	inner.WriteString("\n\n")

	// find hour range from events and tasks
	minHour := 23
	maxHour := 0

	for _, ev := range m.calEvents {
		if ev.AllDay {
			continue
		}
		if h := parseHour(ev.StartTime); h < minHour {
			minHour = h
		}
		if h := parseHour(ev.EndTime); h > maxHour {
			maxHour = h
		}
	}
	for _, t := range m.store.ActiveTasks() {
		if !shouldFireToday(t) {
			continue
		}
		if h := parseHour(t.Time); h >= 0 {
			if h < minHour {
				minHour = h
			}
			if h > maxHour {
				maxHour = h
			}
		}
	}

	if minHour > maxHour {
		minHour = 8
		maxHour = 18
	}
	maxHour++ // include the last hour

	// all-day banner
	for _, ev := range m.calEvents {
		if ev.AllDay {
			inner.WriteString(inputLabelStyle.Render(fmt.Sprintf("  ALL DAY: %s", ev.Summary)))
			inner.WriteString("\n")
		}
	}

	// build timeline
	for hour := minHour; hour <= maxHour; hour++ {
		hourStr := fmt.Sprintf("%02d", hour)

		// find calendar event for this hour
		var evLabel string
		hasEvent := false
		for _, ev := range m.calEvents {
			if ev.AllDay {
				continue
			}
			sh := parseHour(ev.StartTime)
			eh := parseHour(ev.EndTime)
			if hour >= sh && hour < eh {
				hasEvent = true
				if hour == sh {
					evLabel = ev.Summary
				}
			}
		}

		// find task for this hour
		var taskLabel string
		for _, t := range m.store.ActiveTasks() {
			if !shouldFireToday(t) {
				continue
			}
			if parseHour(t.Time) == hour {
				prefix := ""
				if t.Done {
					prefix = "done "
				}
				taskLabel = prefix + t.Desc
				break
			}
		}

		// render row
		leftCol := "·"
		if hasEvent {
			leftCol = inputLabelStyle.Render("████")
		}

		evText := ""
		if evLabel != "" {
			evText = " " + evLabel
		}

		taskText := ""
		if taskLabel != "" {
			taskText = habitDoneStyle.Render("  " + taskLabel)
		}

		line := fmt.Sprintf("  %s  %s%s", hourStr, leftCol, evText)
		if hasEvent {
			inner.WriteString(normalStyle.Render(fmt.Sprintf("  %s  ", hourStr)))
			inner.WriteString(inputLabelStyle.Render("████"))
			if evText != "" {
				inner.WriteString(normalStyle.Render(evText))
			}
		} else {
			inner.WriteString(dimStyle.Render(line))
		}
		if taskText != "" {
			// pad to align task column
			padding := 45 - len(line)
			if padding < 2 {
				padding = 2
			}
			inner.WriteString(strings.Repeat(" ", padding))
			inner.WriteString(taskText)
		}
		inner.WriteString("\n")
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  Left: calendar events  Right: your tasks"))
	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  t:list view  esc:back"))
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

func parseHour(timeStr string) int {
	if len(timeStr) >= 2 {
		h := 0
		for _, c := range timeStr[:2] {
			if c >= '0' && c <= '9' {
				h = h*10 + int(c-'0')
			} else {
				return -1
			}
		}
		return h
	}
	return -1
}

func (m model) renderHabits(w, _ int) string {
	var b strings.Builder

	// header
	modeHint := "f:full history"
	if m.habitFull {
		modeHint = "f:weekly view"
	}
	title := fmt.Sprintf("  Habit Tracker%s%s  h:back",
		strings.Repeat(" ", max(2, w-40)), modeHint)
	b.WriteString(inputLabelStyle.Render(title))
	b.WriteString("\n\n")

	// filter to recurring tasks only (exclude archived)
	var habits []Task
	for _, t := range m.store.Tasks {
		if !t.Archived && t.Recurrence != Once {
			habits = append(habits, t)
		}
	}

	if len(habits) == 0 {
		b.WriteString(dimStyle.Render("  No recurring tasks to track"))
		b.WriteString("\n")
		return b.String()
	}

	// compute date range
	now := time.Now()
	var days int
	if m.habitFull {
		// fit as many days as terminal width allows
		// each day column is 5 chars wide, task name column is ~25 chars
		nameCol := 26
		days = (w - nameCol) / 5
		if days < 7 {
			days = 7
		}
	} else {
		days = 7
	}

	// generate date list (most recent last)
	dates := make([]time.Time, days)
	for i := range dates {
		dates[i] = now.AddDate(0, 0, -(days - 1 - i))
	}

	// task name column width
	nameWidth := 24
	if !m.habitFull {
		// use more space for names in weekly view
		nameWidth = w - (days*5 + 4)
		if nameWidth < 16 {
			nameWidth = 16
		}
		if nameWidth > 40 {
			nameWidth = 40
		}
	}

	// column headers
	hdr := fmt.Sprintf("  %-*s", nameWidth, "Task")
	for _, d := range dates {
		if m.habitFull {
			hdr += fmt.Sprintf("  %2d ", d.Day())
		} else {
			hdr += fmt.Sprintf("  %s", d.Format("Mon"))
		}
	}
	b.WriteString(dimStyle.Render(hdr))
	b.WriteString("\n")

	// separator
	sepLen := nameWidth + 2
	for range dates {
		if m.habitFull {
			sepLen += 5
		} else {
			sepLen += 5
		}
	}
	b.WriteString(dimStyle.Render("  " + strings.Repeat("─", sepLen)))
	b.WriteString("\n")

	// rows
	for _, t := range habits {
		name := t.Desc
		if len(name) > nameWidth {
			name = name[:nameWidth-3] + "..."
		}
		row := fmt.Sprintf("  %-*s", nameWidth, name)

		for _, d := range dates {
			key := d.Format("2006-01-02")
			isToday := key == now.Format("2006-01-02")
			if t.History[key] || (isToday && t.Done) {
				row += "  " + habitDoneStyle.Render("██") + " "
			} else if isToday && t.Dismissed {
				row += "  " + habitDismissStyle.Render("··") + " "
			} else if !shouldFireOnDay(t, d.Weekday()) {
				row += "     "
			} else {
				row += "  " + habitMissStyle.Render("··") + " "
			}
		}

		b.WriteString(normalStyle.Render(row))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	return b.String()
}
