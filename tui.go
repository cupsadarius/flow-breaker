package main

import (
	"fmt"
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
	store      *Store
	cursor     int
	alarm      *alarmState
	fired      map[int]bool
	width      int
	height     int
	msg        string
	msgExpiry  time.Time
	inputMode  bool
	inputField inputField
	inputTime  string
	inputDesc  string
	inputRec   Recurrence
	inputTags  string
	inputDays  [7]bool
	dayCursor    int
	recCursor    int
	editingID    int
	scrollOffset int
	habitView      bool
	habitFull      bool
	confirmDel     bool
	settingsMode   bool
	settingsCursor int
}

type tickMsg time.Time
type macAlertMsg string

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
	return tea.Batch(tickCmd(), pollMacAlert())
}

// ── Update ─────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		m.checkAlarms()
		writeStatusFile(m.store, m.alarm)
		return m, tea.Batch(tickCmd(), pollMacAlert())

	case macAlertMsg:
		m.handleMacDialogResult(string(msg))
		return m, pollMacAlert()

	case tea.KeyMsg:
		if m.confirmDel {
			return m.handleConfirmDel(msg)
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
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.store.Tasks)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g":
		m.cursor = 0
	case "G":
		if len(m.store.Tasks) > 0 {
			m.cursor = len(m.store.Tasks) - 1
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
		if m.cursor >= 0 && m.cursor < len(m.store.Tasks) {
			t := m.store.Tasks[m.cursor]
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
		if len(m.store.Tasks) > 0 {
			m.confirmDel = true
		}
	case "c":
		if m.cursor >= 0 && m.cursor < len(m.store.Tasks) {
			t := &m.store.Tasks[m.cursor]
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
		}
	case "o":
		m.settingsMode = true
		m.settingsCursor = 0
	case "r":
		*m.store = loadStore()
		m.store.resetDaily()
		m.setMsg("Reloaded")
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
		if m.cursor >= 0 && m.cursor < len(m.store.Tasks) {
			m.store.deleteTask(m.store.Tasks[m.cursor].ID)
			if m.cursor >= len(m.store.Tasks) && m.cursor > 0 {
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
	for _, t := range m.store.Tasks {
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
		// responsive column widths
		descWidth := w - 68 // time(8) + seps(12) + repeat(10) + status(13) + habits(22) + padding(3)
		if descWidth < 20 {
			descWidth = 20
		}

		// header with lipgloss border style — STATUS before dots
		hdr := fmt.Sprintf("  %-7s │ %-*s │ %-8s │ %-11s │ %s",
			"TIME", descWidth, "TASK", "REPEAT", "STATUS", "Mo Tu We Th Fr Sa Su")
		sep := fmt.Sprintf("  %s┼%s┼%s┼%s┼%s",
			strings.Repeat("─", 8),
			strings.Repeat("─", descWidth+2),
			strings.Repeat("─", 10),
			strings.Repeat("─", 13),
			strings.Repeat("─", 22))
		b.WriteString(dimStyle.Render(hdr))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(sep))
		b.WriteString("\n")

		if len(m.store.Tasks) == 0 {
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

		taskCount := len(m.store.Tasks)
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
			t := m.store.Tasks[i]
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
					} else {
						dots += habitMissStyle.Render("··")
					}
				}
			} else {
				dots = strings.Repeat("   ", 6) + "  " // 20 chars blank
			}

			// Main line: only plain text — safe to wrap in style.Render()
			mainLine := fmt.Sprintf("  %5s   │ %s %-*s │ %-8s │ %-11s │ ",
				t.Time, icon, maxDesc, desc, rec, status)

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

	if m.inputMode {
		b.WriteString(m.renderInput())
	}

	if m.confirmDel && m.cursor >= 0 && m.cursor < len(m.store.Tasks) {
		t := m.store.Tasks[m.cursor]
		b.WriteString(urgentStyle.Render(fmt.Sprintf("  Delete '%s'? (y/n)", t.Desc)))
		b.WriteString("\n")
	}

	if m.msg != "" && time.Now().Before(m.msgExpiry) {
		b.WriteString(msgStyle.Render("  " + m.msg))
		b.WriteString("\n")
	}

	if !m.inputMode && !m.confirmDel {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  a:add  e:edit  d:del  c:done  h:habits  o:settings  r:reload  j/k:nav  q:quit"))
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

var settingsLabels = [7]string{
	"Snooze duration",
	"Notification",
	"Modal dialog",
	"Text-to-speech",
	"System sound",
	"Terminal bell",
	"Tmux flash",
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
		if m.settingsCursor < 6 {
			m.settingsCursor++
		}
	case "k", "up":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
	case " ", "enter":
		m.toggleSettingBool()
	case "h", "left":
		if m.settingsCursor == 0 && m.store.Settings.SnoozeMins > 1 {
			m.store.Settings.SnoozeMins--
		}
	case "l", "right":
		if m.settingsCursor == 0 && m.store.Settings.SnoozeMins < 60 {
			m.store.Settings.SnoozeMins++
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
	}
}

func (m model) renderSettings(w int) string {
	var inner strings.Builder
	inner.WriteString(inputLabelStyle.Render("  Settings"))
	inner.WriteString("\n\n")

	s := &m.store.Settings
	bools := [7]bool{false, s.AlertNotify, s.AlertDialog, s.AlertSpeech, s.AlertSound, s.AlertBell, s.AlertTmux}

	for i, label := range settingsLabels {
		var val string
		if i == 0 {
			val = fmt.Sprintf("%d min    [◄ ►]", s.SnoozeMins)
		} else if bools[i] {
			val = habitDoneStyle.Render("✓ ON")
		} else {
			val = dimStyle.Render("✗ OFF")
		}

		line := fmt.Sprintf("  %-20s %s", label, val)
		if i == m.settingsCursor {
			inner.WriteString(inputActiveStyle.Render(line))
		} else {
			inner.WriteString(normalStyle.Render(line))
		}
		inner.WriteString("\n")
	}

	inner.WriteString("\n")
	inner.WriteString(dimStyle.Render("  j/k:navigate  space:toggle  ◄►:adjust snooze  esc:back"))
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

func weekStart(t time.Time) time.Time {
	wd := t.Weekday()
	if wd == time.Sunday {
		wd = 7
	}
	d := t.AddDate(0, 0, -int(wd-time.Monday))
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
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

	// filter to recurring tasks only
	var habits []Task
	for _, t := range m.store.Tasks {
		if t.Recurrence != Once {
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
		dates[i] = now.AddDate(0, 0, -(days-1-i))
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

