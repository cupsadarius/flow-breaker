package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Data types & persistence ────────────────────────────────────────────────

type Recurrence string

const (
	Once     Recurrence = "once"
	Daily    Recurrence = "daily"
	Weekdays Recurrence = "weekdays"
	Weekly   Recurrence = "weekly"
	Monthly  Recurrence = "monthly"
	Custom   Recurrence = "custom"
)

type Task struct {
	ID         int             `json:"id"`
	Time       string          `json:"time"`
	Desc       string          `json:"desc"`
	Recurrence Recurrence      `json:"recurrence"`
	Days       []string        `json:"days,omitempty"`
	Done       bool            `json:"done"`
	Dismissed  bool            `json:"dismissed"`
	Snoozed    int64           `json:"snoozed"`
	Tags       []string        `json:"tags,omitempty"`
	CreatedAt  string          `json:"created_at"`
	History    map[string]bool `json:"history,omitempty"`
	Archived   bool            `json:"archived,omitempty"`
	ArchivedAt string          `json:"archived_at,omitempty"`
	// Source records who created/last-mutated the task. One of:
	// tui/cli/ntfy/claude-code/opencode/codex/phone/api/calendar.
	Source string `json:"source,omitempty"`
}

type Settings struct {
	SnoozeMins  int  `json:"snooze_mins"`
	AlertNotify bool `json:"alert_notify"`
	AlertDialog bool `json:"alert_dialog"`
	AlertSpeech bool `json:"alert_speech"`
	AlertSound  bool `json:"alert_sound"`
	AlertBell   bool `json:"alert_bell"`
	AlertTmux   bool `json:"alert_tmux"`
	// Calendar feed integration
	CalEnabled   bool `json:"cal_enabled"`
	CalCacheMins int  `json:"cal_cache_mins"`
}

func defaultSettings() Settings {
	return Settings{
		SnoozeMins:   5,
		AlertNotify:  true,
		AlertDialog:  true,
		AlertSpeech:  true,
		AlertSound:   true,
		AlertBell:    true,
		AlertTmux:    true,
		CalEnabled:   false,
		CalCacheMins: 15,
	}
}

type Store struct {
	Tasks     []Task   `json:"tasks"`
	LastReset string   `json:"last_reset"`
	NextID    int      `json:"next_id"`
	Settings  Settings `json:"settings"`

	// mu guards concurrent access. encoding/json ignores unexported fields.
	mu sync.RWMutex `json:"-"`
}

func dataDir() string {
	dir := os.Getenv("FLOW_BREAKER_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".flow-breaker")
	}
	os.MkdirAll(dir, 0o755)
	return dir
}

func dataPath() string   { return filepath.Join(dataDir(), "tasks.json") }
func statusPath() string { return filepath.Join(dataDir(), "status.json") }
func sockPath() string   { return filepath.Join(dataDir(), "flow.sock") }

func loadStore() *Store {
	s := &Store{}
	data, err := os.ReadFile(dataPath())
	if err != nil {
		s.NextID = 1
		return s
	}
	json.Unmarshal(data, s)
	if s.NextID == 0 {
		s.NextID = 1
	}
	if s.Settings.SnoozeMins == 0 {
		s.Settings = defaultSettings()
	}
	return s
}

// save acquires the write lock and persists the store.
func (s *Store) save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

// saveLocked persists the store; the caller must already hold s.mu.
func (s *Store) saveLocked() {
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(dataPath(), data, 0o644)
}

func (s *Store) addTask(timeStr, desc string, rec Recurrence, tags, days []string) Task {
	return s.addTaskSource(timeStr, desc, rec, tags, days, "")
}

// addTaskSource is like addTask but stamps the Source field. It acquires the
// write lock once and persists.
func (s *Store) addTaskSource(timeStr, desc string, rec Recurrence, tags, days []string, source string) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Task{
		ID:         s.NextID,
		Time:       timeStr,
		Desc:       desc,
		Recurrence: rec,
		Days:       days,
		Tags:       tags,
		CreatedAt:  time.Now().Format(time.RFC3339),
		Source:     source,
	}
	s.NextID++
	s.Tasks = append(s.Tasks, t)
	s.sortTasks()
	s.saveLocked()
	return t
}

// markDone sets Done=true, stamps Source, and persists. Returns the updated
// task, or an error if the id is not found. Acquires the write lock once.
func (s *Store) markDone(id int, source string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findTaskByID(id)
	if err != nil {
		return Task{}, err
	}
	s.Tasks[i].Done = true
	s.Tasks[i].Source = source
	s.saveLocked()
	return s.Tasks[i], nil
}

// snoozeTask sets Snoozed to now+minutes, stamps Source, and persists. When
// minutes <= 0 it falls back to Settings.SnoozeMins (resolved under the lock to
// avoid a race). Returns the updated task or an error if the id is not found.
func (s *Store) snoozeTask(id int, minutes int, source string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findTaskByID(id)
	if err != nil {
		return Task{}, err
	}
	if minutes <= 0 {
		minutes = s.Settings.SnoozeMins
	}
	s.Tasks[i].Snoozed = time.Now().Add(time.Duration(minutes) * time.Minute).Unix()
	s.Tasks[i].Source = source
	s.saveLocked()
	return s.Tasks[i], nil
}

// editTaskSource applies each non-nil field to the task, stamps Source, and
// persists. A nil pointer leaves that field unchanged (partial edit). Returns
// the updated task or an error if the id is not found. Acquires the write lock
// once; re-resolves the index after sorting since sortTasks may reorder.
func (s *Store) editTaskSource(id int, timeStr, desc *string, rec *Recurrence, tags, days *[]string, source string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findTaskByID(id)
	if err != nil {
		return Task{}, err
	}
	if timeStr != nil {
		s.Tasks[i].Time = *timeStr
	}
	if desc != nil {
		s.Tasks[i].Desc = *desc
	}
	// An empty-string recurrence means "leave unchanged" (mirrors how add
	// defaults it), so a present-but-blank field never clobbers the value.
	if rec != nil && *rec != "" {
		s.Tasks[i].Recurrence = *rec
	}
	if tags != nil {
		s.Tasks[i].Tags = *tags
	}
	if days != nil {
		s.Tasks[i].Days = *days
	}
	s.Tasks[i].Source = source
	s.sortTasks()
	s.saveLocked()
	i, err = s.findTaskByID(id)
	if err != nil {
		return Task{}, err
	}
	if i < 0 {
		return Task{}, fmt.Errorf("task %d not found", id)
	}
	// Returned Task is a value copy; its Days/Tags/History still alias the
	// stored slices/map. Safe today because writers reassign slices wholesale
	// (no in-place History writer exists yet).
	return s.Tasks[i], nil
}

func (s *Store) editTask(id int, timeStr, desc string, rec Recurrence, tags, days []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks[i].Time = timeStr
			s.Tasks[i].Desc = desc
			s.Tasks[i].Recurrence = rec
			s.Tasks[i].Tags = tags
			s.Tasks[i].Days = days
			break
		}
	}
	s.sortTasks()
	s.saveLocked()
}

func (s *Store) deleteTask(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.Tasks {
		if t.ID == id {
			s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			break
		}
	}
	s.saveLocked()
}

// deleteTaskChecked removes the task with the given id, persisting the store.
// Returns an error if the id is not found (so callers can 404). Acquires the
// write lock once.
func (s *Store) deleteTaskChecked(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findTaskByID(id)
	if err != nil {
		return err
	}
	s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
	s.saveLocked()
	return nil
}

// findTaskByID returns the index of the task with the given id, or an error.
// It takes NO lock; the caller must hold s.mu (the returned index is only valid
// while that lock is held).
func (s *Store) findTaskByID(id int) (int, error) {
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("task %d not found", id)
}

// ActiveTasks returns non-archived tasks.
func (s *Store) ActiveTasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Task
	for _, t := range s.Tasks {
		if !t.Archived {
			out = append(out, t)
		}
	}
	return out
}

// ArchivedTasks returns archived tasks, most recently archived first.
func (s *Store) ArchivedTasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Task
	for _, t := range s.Tasks {
		if t.Archived {
			out = append(out, t)
		}
	}
	// sort by ArchivedAt descending
	sort.Slice(out, func(i, j int) bool {
		return out[i].ArchivedAt > out[j].ArchivedAt
	})
	return out
}

// activeIndex maps a position in the active-only view to the real index in s.Tasks.
func (s *Store) activeIndex(activePos int) int {
	count := 0
	for i, t := range s.Tasks {
		if t.Archived {
			continue
		}
		if count == activePos {
			return i
		}
		count++
	}
	return -1
}

func (s *Store) sortTasks() {
	sort.Slice(s.Tasks, func(i, j int) bool {
		return s.Tasks[i].Time < s.Tasks[j].Time
	})
}

func (s *Store) importCalendarEvents(events []CalendarEvent) []Task {
	var imported []Task
	for _, ev := range events {
		if ev.AllDay {
			continue
		}
		// deduplicate: skip if an active task with same time+desc+gcal tag already exists
		dup := false
		for _, t := range s.Tasks {
			if !t.Archived && t.Time == ev.StartTime && t.Desc == ev.Summary && hasTag(t.Tags, "gcal") {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		t := s.addTask(ev.StartTime, ev.Summary, Once, []string{"gcal"}, nil)
		imported = append(imported, t)
	}
	return imported
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (s *Store) resetDaily() {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if s.LastReset == today {
		return
	}
	now := time.Now()
	// record yesterday's completions in history before clearing
	for i := range s.Tasks {
		if s.Tasks[i].Done {
			if s.Tasks[i].History == nil {
				s.Tasks[i].History = make(map[string]bool)
			}
			s.Tasks[i].History[s.LastReset] = true
		}
	}
	// archive once-tasks that were created before today
	for i := range s.Tasks {
		if s.Tasks[i].Archived {
			continue
		}
		if s.Tasks[i].Recurrence == Once {
			createdDate := createdOnDate(s.Tasks[i])
			if createdDate.Before(startOfDay(now)) {
				s.Tasks[i].Archived = true
				s.Tasks[i].ArchivedAt = now.Format(time.RFC3339)
			}
		}
	}
	for i := range s.Tasks {
		if s.Tasks[i].Archived {
			continue
		}
		s.Tasks[i].Done = false
		s.Tasks[i].Dismissed = false
		s.Tasks[i].Snoozed = 0
	}
	s.LastReset = today
	s.saveLocked()
}

// createdOnDate parses the CreatedAt field and returns the date portion.
// Falls back to epoch if parsing fails (so the task gets archived).
func createdOnDate(t Task) time.Time {
	if t.CreatedAt != "" {
		parsed, err := time.Parse(time.RFC3339, t.CreatedAt)
		if err == nil {
			return startOfDay(parsed)
		}
	}
	return time.Time{} // epoch — will always be before today
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

var weekdayShort = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func shouldFireToday(t Task) bool {
	return shouldFireTodayAt(t, time.Now())
}

// shouldFireTodayAt is shouldFireToday with an injectable clock (used by the
// daemon scheduler so firing can be evaluated deterministically).
func shouldFireTodayAt(t Task, now time.Time) bool {
	if t.Archived {
		return false
	}
	return shouldFireOnDay(t, now.Weekday())
}

func shouldFireOnDay(t Task, wd time.Weekday) bool {
	if len(t.Days) > 0 {
		today := weekdayShort[wd]
		for _, d := range t.Days {
			if d == today {
				return true
			}
		}
		return false
	}
	switch t.Recurrence {
	case Weekdays:
		return wd >= time.Monday && wd <= time.Friday
	default:
		return true
	}
}

func parseTaskTime(s string) (time.Time, bool) {
	return parseTaskTimeAt(s, time.Now())
}

// parseTaskTimeAt is parseTaskTime with an injectable clock: it resolves the
// task's HH:MM against now's date/location.
func parseTaskTimeAt(s string, now time.Time) (time.Time, bool) {
	for _, f := range []string{"15:04", "3:04PM", "3:04 PM", "1504"} {
		t, err := time.Parse(f, strings.ToUpper(s))
		if err == nil {
			return time.Date(now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), 0, 0, now.Location()), true
		}
	}
	return time.Time{}, false
}

func timeUntil(t Task) time.Duration {
	return timeUntilAt(t, time.Now())
}

// timeUntilAt is timeUntil with an injectable clock.
func timeUntilAt(t Task, now time.Time) time.Duration {
	target, ok := parseTaskTimeAt(t.Time, now)
	if !ok {
		return 999 * time.Hour
	}
	return target.Sub(now)
}

func nextOccurrenceLabel(t Task) string {
	now := time.Now()
	wd := now.Weekday()
	for i := 1; i <= 7; i++ {
		next := time.Weekday((int(wd) + i) % 7)
		if shouldFireOnDay(t, next) {
			short := weekdayShort[next]
			return "next " + strings.ToUpper(short[:1]) + short[1:]
		}
	}
	return ""
}

func fmtDuration(d time.Duration) string {
	neg := d < 0
	if neg {
		d = -d
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	sign := ""
	if neg {
		sign = "-"
	}
	if h > 0 {
		return fmt.Sprintf("%s%dh %02dm", sign, h, m)
	}
	return fmt.Sprintf("%s%dm %02ds", sign, m, s)
}
