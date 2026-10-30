package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TodayGroups splits today's tasks by state, each group in time order.
type TodayGroups struct{ Overdue, Next, Later, Done []Task }

func groupToday(tasks []Task, now time.Time) TodayGroups {
	var today []Task
	for _, t := range tasks {
		if shouldFireTodayAt(t, now) {
			today = append(today, t)
		}
	}
	sort.SliceStable(today, func(i, j int) bool { return timeUntilAt(today[i], now) < timeUntilAt(today[j], now) })

	var g TodayGroups
	for _, t := range today {
		switch {
		case t.Done || t.Dismissed:
			g.Done = append(g.Done, t)
		case timeUntilAt(t, now) < 0:
			g.Overdue = append(g.Overdue, t)
		case len(g.Next) == 0:
			g.Next = append(g.Next, t)
		default:
			g.Later = append(g.Later, t)
		}
	}
	return g
}

type pane int

const (
	paneToday pane = iota
	paneHabits
	paneInbox
)

// todayRow is one selectable line of the Today pane. task is nil on the
// collapsible DONE row.
type todayRow struct {
	task   *Task
	group  string // group heading printed above this row; "" when none
	status string
	style  lipgloss.Style
}

func (m model) todayRows() []todayRow {
	now := time.Now()
	g := groupToday(m.store.ActiveTasks(), now)
	var rows []todayRow
	add := func(group string, ts []Task, style lipgloss.Style, status func(Task) string) {
		for i := range ts {
			r := todayRow{task: &ts[i], status: status(ts[i]), style: style}
			if i == 0 {
				r.group = group
			}
			rows = append(rows, r)
		}
	}
	add("OVERDUE", g.Overdue, urgentStyle, func(t Task) string { return fmtShort(-timeUntilAt(t, now)) + " ago" })
	add("NEXT", g.Next, nextStyle.PaddingLeft(0), func(t Task) string { return "in " + fmtShort(timeUntilAt(t, now)) })
	add("LATER", g.Later, normalStyle, func(t Task) string { return "in " + fmtShort(timeUntilAt(t, now)) })
	if len(g.Done) > 0 {
		label := fmt.Sprintf("DONE (%d)  ▸ expand", len(g.Done))
		if m.doneOpen {
			label = fmt.Sprintf("DONE (%d)  ▾ collapse", len(g.Done))
		}
		rows = append(rows, todayRow{group: label, style: dimStyle})
		if m.doneOpen {
			add("", g.Done, doneStyle, func(t Task) string {
				if t.Dismissed {
					return "dismissed"
				}
				return "done"
			})
		}
	}
	return rows
}

// fmtShort renders a duration as "1h30" or "45m".
func fmtShort(d time.Duration) string {
	d = d.Round(time.Minute)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02d", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func (m model) selectedTask() (Task, bool) {
	rows := m.todayRows()
	if m.focus != paneToday || m.cursor >= len(rows) || rows[m.cursor].task == nil {
		return Task{}, false
	}
	return *rows[m.cursor].task, true
}

// moveCursor applies a navigation key and clamps the cursor to [0, n-1].
func moveCursor(c, n int, key string) int {
	switch key {
	case "j", "down":
		c++
	case "k", "up":
		c--
	case "g":
		c = 0
	case "G":
		c = n - 1
	}
	return max(0, min(c, n-1))
}

func (m model) handleToday(key string) (tea.Model, tea.Cmd) {
	rows := m.todayRows()
	m.cursor = moveCursor(m.cursor, len(rows), key)
	if key == "enter" && m.cursor < len(rows) && rows[m.cursor].task == nil {
		m.doneOpen = !m.doneOpen
		return m, nil
	}
	t, ok := m.selectedTask()
	if !ok || !slices.Contains([]string{"enter", "s", "e", "d", "x"}, key) {
		return m, nil
	}
	if m.offline {
		m.setMsg("daemon down")
		return m, nil
	}
	body := map[string]any{"source": "tui"}
	switch key {
	case "enter":
		if t.Done {
			return m, taskActionCmd("POST", fmt.Sprintf("/task/%d/undo", t.ID), body, "Marked not done")
		}
		return m, taskActionCmd("POST", fmt.Sprintf("/task/%d/done", t.ID), body, "Marked done")
	case "s":
		return m, taskActionCmd("POST", fmt.Sprintf("/task/%d/snooze", t.ID), body, "Snoozed")
	case "e":
		m.startInput(t)
	case "d", "x":
		m.confirmDel, m.targetID = true, t.ID
	}
	return m, nil
}

// handleHabits moves in the Habits pane; enter jumps to the habit's next open
// slot in Today.
func (m model) handleHabits(key string) (tea.Model, tea.Cmd) {
	m.habitCursor = moveCursor(m.habitCursor, len(m.habits), key)
	if key != "enter" || len(m.habits) == 0 {
		return m, nil
	}
	ids := m.habits[m.habitCursor].TaskIDs
	for i, r := range m.todayRows() {
		if r.task != nil && !r.task.Done && !r.task.Dismissed && slices.Contains(ids, r.task.ID) {
			m.focus, m.cursor = paneToday, i
			return m, nil
		}
	}
	m.setMsg("No open slot today")
	return m, nil
}

func (m model) footerKeys() string {
	switch m.focus {
	case paneHabits:
		return "⏎ jump to today  tab next pane  / add  ? more  q quit"
	case paneInbox:
		return "⏎ schedule  d delete  tab next pane  / add  ? more"
	}
	return "⏎ done  s snooze  e edit  tab next pane  / add  ? more"
}

// paneTitle renders "─ NAME ──── info ─" across width w.
func paneTitle(name, info string, w int, focused bool) string {
	style := dimStyle
	if focused {
		style = inputActiveStyle
	}
	left := "─ " + name + " "
	right := " " + info + " ─"
	fill := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	return style.Render(left + strings.Repeat("─", fill) + right)
}

// clip cuts s to n display columns.
func clip(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// box pads or crops lines to exactly w×h.
func box(lines []string, w, h int) string {
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(w).Height(h).Render(strings.Join(lines, "\n"))
}

func (m model) renderPanes(w, h int) string {
	h = max(h, 6)
	leftW := w * 45 / 100
	rightW := w - leftW - 1
	habitsH := h / 2
	sep := dimStyle.Render(strings.TrimSuffix(strings.Repeat("│\n", h), "\n"))
	right := lipgloss.JoinVertical(lipgloss.Left, m.renderHabitsPane(rightW, habitsH), m.renderInboxPane(rightW, h-habitsH))
	return lipgloss.JoinHorizontal(lipgloss.Top, m.renderTodayPane(leftW, h), sep, right)
}

func (m model) renderTodayPane(w, h int) string {
	focused := m.focus == paneToday
	rows := m.todayRows()
	g := groupToday(m.store.ActiveTasks(), time.Now())
	total := len(g.Overdue) + len(g.Next) + len(g.Later) + len(g.Done)
	lines := []string{paneTitle("TODAY", fmt.Sprintf("%d/%d", len(g.Done), total), w, focused)}
	if len(rows) == 0 {
		lines = append(lines, dimStyle.Render(" No tasks today — press a to add one"))
	}
	cursorLine := 0
	for i, r := range rows {
		selected := focused && i == m.cursor
		if r.task == nil {
			if selected {
				cursorLine = len(lines)
				lines = append(lines, cursorStyle.Render(clip(" "+r.group, w)))
			} else {
				lines = append(lines, r.style.Bold(true).Render(clip(" "+r.group, w)))
			}
			continue
		}
		if r.group != "" {
			lines = append(lines, r.style.Bold(true).Render(" "+r.group))
		}
		icon := " "
		if selected {
			icon = "▸"
			cursorLine = len(lines)
		}
		left := fmt.Sprintf(" %s %5s  ", icon, r.task.Time)
		descW := max(4, w-lipgloss.Width(left)-lipgloss.Width(r.status)-2)
		line := left + fmt.Sprintf("%-*s", descW, clip(r.task.Desc, descW)) + " " + r.status
		if selected {
			lines = append(lines, cursorStyle.Render(clip(line, w)))
			detail := string(r.task.Recurrence)
			if len(r.task.Tags) > 0 {
				detail = "#" + strings.Join(r.task.Tags, " #") + " · " + detail
			}
			lines = append(lines, dimStyle.Render(clip("          "+detail, w)))
		} else {
			lines = append(lines, r.style.Render(clip(line, w)))
		}
	}
	if off := cursorLine - (h - 2); off > 0 {
		lines = append(lines[:1], lines[1+off:]...)
	}
	return box(lines, w, h)
}

func (m model) renderHabitsPane(w, h int) string {
	focused := m.focus == paneHabits
	weeks := 4
	if w < 75 {
		weeks = 2
	}
	lines := []string{paneTitle("HABITS", fmt.Sprintf("streak · %d weeks", weeks), w, focused)}
	if len(m.habits) == 0 {
		lines = append(lines, dimStyle.Render(" No habits yet — repeat a task to start one"))
		return box(lines, w, h)
	}
	done, scheduled := 0, 0
	for i, hb := range m.habits {
		done += hb.WeekDone
		scheduled += hb.WeekScheduled
		marker := " "
		if focused && i == m.habitCursor {
			marker = "▸"
		}
		streak := dimStyle.Render(fmt.Sprintf("   %2d", hb.Streak))
		if hb.Streak > 0 {
			streak = nextStyle.PaddingLeft(0).Render(fmt.Sprintf("🔥 %2d", hb.Streak))
		}
		var grid strings.Builder
		for j, c := range hb.Cells[len(hb.Cells)-7*weeks:] {
			if j > 0 && j%7 == 0 {
				grid.WriteString(" ")
			}
			switch c {
			case CellDone:
				grid.WriteString(habitDoneStyle.Render("▪"))
			case CellMissed:
				grid.WriteString(habitMissStyle.Render("▪"))
			default:
				grid.WriteString(dimStyle.Render("·"))
			}
		}
		nameW := max(6, w-lipgloss.Width(streak)-8*weeks-6)
		name := fmt.Sprintf("%s %-*s ", marker, nameW, clip(hb.Name, nameW))
		if focused && i == m.habitCursor {
			name = cursorStyle.Render(name)
		}
		lines = append(lines, name+streak+"  "+grid.String())
	}
	if scheduled > 0 {
		lines = append(lines, "", dimStyle.Render(fmt.Sprintf("  this week %d/%d · %d%%", done, scheduled, done*100/scheduled)))
	}
	if off := m.habitCursor - (h - 3); focused && off > 0 {
		lines = append(lines[:1], lines[1+off:]...)
	}
	return box(lines, w, h)
}

func (m model) renderInboxPane(w, h int) string {
	focused := m.focus == paneInbox
	lines := []string{paneTitle("INBOX", fmt.Sprint(len(m.inbox)), w, focused)}
	if len(m.inbox) == 0 {
		lines = append(lines, dimStyle.Render(" Inbox empty"))
	}
	for i, t := range m.inbox {
		line := clip(" 📥 "+t.Desc, w)
		if focused && i == m.inboxCursor {
			line = cursorStyle.Render(line)
		}
		lines = append(lines, line)
	}
	if off := m.inboxCursor - (h - 2); focused && off > 0 {
		lines = append(lines[:1], lines[1+off:]...)
	}
	return box(lines, w, h)
}

type barMode int

const (
	barOff barMode = iota
	barAdd
	barSchedule
)

// targetDesc names the task or inbox item m.targetID points at.
func (m model) targetDesc() string {
	for _, t := range append(m.store.ActiveTasks(), m.inbox...) {
		if t.ID == m.targetID {
			return t.Desc
		}
	}
	return fmt.Sprintf("#%d", m.targetID)
}

func (m model) selectedInbox() (Task, bool) {
	if m.focus != paneInbox || m.inboxCursor >= len(m.inbox) {
		return Task{}, false
	}
	return m.inbox[m.inboxCursor], true
}

func (m model) handleInbox(key string) (tea.Model, tea.Cmd) {
	m.inboxCursor = moveCursor(m.inboxCursor, len(m.inbox), key)
	t, ok := m.selectedInbox()
	if !ok {
		return m, nil
	}
	switch key {
	case "enter":
		m.bar, m.barText, m.targetID = barSchedule, "", t.ID
	case "d", "x":
		m.confirmDel, m.targetID = true, t.ID
	}
	return m, nil
}

// handleBar edits the quick-add line; enter sends it as a capture or as the
// schedule text for the selected inbox item.
func (m model) handleBar(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Paste {
		m.barText += string(msg.Runes)
		return m, nil
	}
	switch key := msg.String(); key {
	case "esc":
		m.bar = barOff
	case "ctrl+e":
		if m.bar == barAdd {
			m.bar = barOff
			m.startInput(Task{Desc: m.barText, Recurrence: Daily})
		}
	case "backspace", "ctrl+h":
		if r := []rune(m.barText); len(r) > 0 {
			m.barText = string(r[:len(r)-1])
		}
	case "enter":
		text := strings.TrimSpace(m.barText)
		mode := m.bar
		m.bar = barOff
		if text == "" {
			return m, nil
		}
		if m.offline {
			m.setMsg("daemon down")
			return m, nil
		}
		if mode == barAdd {
			return m, captureCmd(text)
		}
		return m, scheduleCmd(m.targetID, text)
	default:
		if len(msg.Runes) > 0 {
			m.barText += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) barLine() string {
	switch m.bar {
	case barAdd:
		return inputActiveStyle.Render(" + " + m.barText + "█")
	case barSchedule:
		return inputActiveStyle.Render(" when? "+m.barText+"█") + dimStyle.Render("  e.g. at 15:00, tomorrow 9:30")
	}
	return dimStyle.Render(` + add: "standup every weekday at 9:30"  (/)`)
}

// captureCmd sends free text to POST /capture and reports the daemon's echo.
func captureCmd(text string) tea.Cmd {
	return func() tea.Msg {
		var out struct {
			Echo string `json:"echo"`
		}
		err := newClientTimeout(10*time.Second).Post("/capture", map[string]any{"text": text, "source": "tui"}, &out)
		return actionMsg{ok: out.Echo, err: err}
	}
}

func scheduleCmd(id int, text string) tea.Cmd {
	return func() tea.Msg {
		err := newClientTimeout(10*time.Second).Post(fmt.Sprintf("/inbox/%d/schedule", id), map[string]any{"text": text, "source": "tui"}, nil)
		if err != nil && strings.Contains(err.Error(), "no time found") {
			return actionMsg{err: fmt.Errorf("no time found — try 'at 15:00'")}
		}
		return actionMsg{ok: "Scheduled", err: err}
	}
}

// handleHelp routes the keys of the ? overlay, which also opens the screens
// that have no key on the main screen.
func (m model) handleHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.helpOpen = false
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "v":
		if len(m.store.ArchivedTasks()) == 0 {
			m.setMsg("No archived tasks")
			return m, nil
		}
		m.archiveMode, m.archiveCursor = true, 0
	case "f":
		m.feedsMode, m.feedsCursor = true, 0
		m.feedsList, _ = loadFeeds()
	case "o":
		m.settingsMode, m.settingsCursor = true, 0
	}
	return m, nil
}

func renderHelp() string {
	rows := [][2]string{
		{"tab / shift+tab", "move between Today, Habits, Inbox"},
		{"j k g G", "move in the pane"},
		{"/ or a", "quick add (ctrl+e: full form)"},
		{"⏎", "Today: done · Habits: jump to slot · Inbox: schedule"},
		{"s", "snooze (Today)"},
		{"e", "edit (Today)"},
		{"d", "delete (Today, Inbox)"},
		{"⏎ on DONE", "expand or collapse done tasks"},
		{"alarm", "space dismiss · s snooze · c done"},
		{"", ""},
		{"v", "archive"},
		{"f", "calendar feeds"},
		{"o", "settings"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(inputLabelStyle.Render("  Keys") + "\n\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %s  %s\n", inputActiveStyle.Render(fmt.Sprintf("%-16s", r[0])), r[1]))
	}
	b.WriteString("\n" + dimStyle.Render("  any other key closes"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("14")).Padding(1, 2).Render(b.String()) + "\n"
}
