package main

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
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
	Date       string          `json:"date,omitempty"` // YYYY-MM-DD; empty = today / every applicable recurrence day
	Desc       string          `json:"desc"`
	Recurrence Recurrence      `json:"recurrence"`
	Days       []string        `json:"days,omitempty"`
	Done       bool            `json:"done"`
	Dismissed  bool            `json:"dismissed"`
	Missed     bool            `json:"missed,omitempty"` // escalation ladder gave up after a 2nd run
	Snoozed    int64           `json:"snoozed"`
	Tags       []string        `json:"tags,omitempty"`
	CreatedAt  string          `json:"created_at"`
	History    map[string]bool `json:"history,omitempty"`
	Archived   bool            `json:"archived,omitempty"`
	ArchivedAt string          `json:"archived_at,omitempty"`
	// Source records who created/last-mutated the task. One of:
	// tui/cli/ntfy/claude-code/opencode/codex/phone/api/calendar.
	Source string `json:"source,omitempty"`
	// CalUID links a materialized calendar task back to its source iCal event
	// (CalendarEvent.UID). It is the upsert key for materializeCalendar.
	CalUID string `json:"cal_uid,omitempty"`
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
	Inbox     []Task   `json:"inbox"` // timeless captures; immortal (never reset, never archived)
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

func dataPath() string { return filepath.Join(dataDir(), "tasks.json") }

func loadStore() *Store {
	// Seed defaults up front so a fresh data dir (no tasks.json yet) still yields
	// working settings — otherwise a brand-new daemon would serve GET /settings
	// with all alerts off and SnoozeMins:0. A present file overrides these.
	s := &Store{NextID: 1, Settings: defaultSettings()}
	// Sweep temp files orphaned by a hard crash between CreateTemp and Rename in
	// saveLocked, so they don't accumulate. Best-effort.
	if matches, err := filepath.Glob(filepath.Join(dataDir(), "tasks.json.*.tmp")); err == nil {
		for _, m := range matches {
			os.Remove(m)
		}
	}
	data, err := os.ReadFile(dataPath())
	if err != nil {
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

// saveLocked persists the store atomically; the caller must already hold s.mu.
// It marshals, writes to a temp file in the SAME dir, fsyncs, then renames over
// tasks.json — so a mid-write crash can never truncate or corrupt the sole
// durable copy. On any error the temp file is cleaned up and no partial
// tasks.json is left behind.
func (s *Store) saveLocked() {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("flow-breaker: save failed (marshal): %v", err)
		return
	}
	dir := dataDir()
	f, err := os.CreateTemp(dir, "tasks.json.*.tmp")
	if err != nil {
		log.Printf("flow-breaker: save failed (create temp): %v", err)
		return
	}
	tmp := f.Name()
	// Best-effort cleanup: harmless no-op after a successful rename.
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		log.Printf("flow-breaker: save failed (write %s): %v", tmp, err)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		log.Printf("flow-breaker: save failed (fsync %s): %v", tmp, err)
		return
	}
	if err := f.Close(); err != nil {
		log.Printf("flow-breaker: save failed (close %s): %v", tmp, err)
		return
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		log.Printf("flow-breaker: save failed (chmod %s): %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, dataPath()); err != nil {
		log.Printf("flow-breaker: save failed (rename %s): %v", tmp, err)
		return
	}
	// fsync the parent dir so the rename's directory entry is durable across a
	// power loss (otherwise the fsync'd data file may be lost, showing the OLD
	// tasks.json despite a "successful" save). Best-effort.
	if d, err := os.Open(dir); err != nil {
		log.Printf("flow-breaker: save warning (open dir %s): %v", dir, err)
	} else {
		if err := d.Sync(); err != nil {
			log.Printf("flow-breaker: save warning (fsync dir %s): %v", dir, err)
		}
		d.Close()
	}
}

func (s *Store) addTask(timeStr, desc string, rec Recurrence, tags, days []string) Task {
	return s.addTaskSource(timeStr, "", desc, rec, tags, days, "")
}

// addTaskSource is like addTask but stamps the Source field. date is the
// optional YYYY-MM-DD the task fires on ("" = today / every applicable
// recurrence day). It acquires the write lock once and persists.
func (s *Store) addTaskSource(timeStr, date, desc string, rec Recurrence, tags, days []string, source string) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Task{
		ID:         s.NextID,
		Time:       timeStr,
		Date:       date,
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

// addInbox appends a timeless capture to the immortal inbox. It allocates an id
// from NextID, stamps Desc/Source/CreatedAt, and persists atomically (locks once
// + saveLocked). Inbox items are never reset or archived.
func (s *Store) addInbox(desc, source string) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Task{
		ID:        s.NextID,
		Desc:      desc,
		Source:    source,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	s.NextID++
	s.Inbox = append(s.Inbox, t)
	s.saveLocked()
	return t
}

// InboxItems returns a copy of the inbox.
func (s *Store) InboxItems() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Task{}, s.Inbox...)
}

func (s *Store) findInboxByID(id int) (int, error) {
	for i := range s.Inbox {
		if s.Inbox[i].ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("inbox item %d not found", id)
}

// scheduleInbox moves an inbox item to the tasks, timed by res. The task keeps
// the item's ID and Desc.
func (s *Store) scheduleInbox(id int, res ParseResult, source string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findInboxByID(id)
	if err != nil {
		return Task{}, err
	}
	t := s.Inbox[i]
	t.Time, t.Date, t.Recurrence, t.Days, t.Source = res.Time, res.Date, res.Recurrence, res.Days, source
	t.CreatedAt = time.Now().Format(time.RFC3339) // habits start counting from the schedule day
	s.Inbox = append(s.Inbox[:i], s.Inbox[i+1:]...)
	s.Tasks = append(s.Tasks, t)
	s.sortTasks()
	s.saveLocked()
	return t, nil
}

func (s *Store) deleteInbox(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findInboxByID(id)
	if err != nil {
		return err
	}
	s.Inbox = append(s.Inbox[:i], s.Inbox[i+1:]...)
	s.saveLocked()
	return nil
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

// dismissTask sets Dismissed=true, stamps Source, and persists. Returns the
// updated task, or an error if the id is not found. Acquires the write lock
// once. Mirrors markDone.
func (s *Store) dismissTask(id int, source string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findTaskByID(id)
	if err != nil {
		return Task{}, err
	}
	s.Tasks[i].Dismissed = true
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

// markMissed sets Missed=true and persists. Returns the updated task, or an
// error if the id is not found. Acquires the write lock once. Used by the
// escalation ladder's terminal step when a task has already auto-snoozed once:
// the second ladder gives up and the task is recorded as missed (no more push).
func (s *Store) markMissed(id int) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.findTaskByID(id)
	if err != nil {
		return Task{}, err
	}
	s.Tasks[i].Missed = true
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
	// stored slices/map. Safe because every writer replaces them wholesale.
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

// AllTasks returns a copy of every task (active and archived).
func (s *Store) AllTasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, len(s.Tasks))
	copy(out, s.Tasks)
	return out
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

// calendarAlarmTime computes a materialized calendar task's Time/Date from an
// event's "HH:MM" start, applying the fixed 10-minute lead. It resolves the
// start against now's date and subtracts 10m. If that lead would cross to the
// day BEFORE now's date (e.g. a 00:05 event → yesterday 23:55, which would
// surface as instantly past-missed), it clamps to the event start on now's date
// with no lead — so the alarm fires at event time rather than as already-overdue.
// Returns ok=false if start is not parseable as HH:MM.
func calendarAlarmTime(start string, now time.Time) (timeStr, dateStr string, ok bool) {
	t, err := time.Parse("15:04", start)
	if err != nil {
		return "", "", false
	}
	base := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	alarm := base.Add(-10 * time.Minute)
	// Clamp a cross-midnight lead back to the event start (no lead) so it never
	// materializes onto the previous day as an immediately-missed alarm.
	if alarm.Day() != base.Day() {
		alarm = base
	}
	return alarm.Format("15:04"), alarm.Format("2006-01-02"), true
}

// materializeCalendar upserts today's eligible timed calendar events into
// s.Tasks as ordinary Once tasks (Source "calendar", CalUID = event UID) so they
// flow through the normal firing/escalation/ack pipeline. It locks once and
// persists via saveLocked; because it holds the write lock, the add path is
// inlined rather than calling addTaskSource (which would re-lock and deadlock).
//
// Rules:
//   - Eligible = !AllDay && !Declined && StartTime != "" (and a non-empty UID to
//     upsert against). Others are skipped.
//   - Upsert by CalUID: an existing task with CalUID == ev.UID has its Time/Date/
//     Desc refreshed if changed (counts as updated); its Done/Snoozed/Missed/
//     Dismissed ack state is PRESERVED (a Done calendar task is a tombstone — it
//     stays Done and never re-surfaces). A new UID is appended (counts as added).
//   - Reconcile: a Source "calendar" task whose CalUID vanished from the eligible
//     set is deleted if un-acted (!Done, counts as dropped); a Done one is kept
//     (harmless — the daily reset archives it).
func (s *Store) materializeCalendar(events []CalendarEvent, now time.Time) (added, updated, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	eligible := make(map[string]bool)
	for _, ev := range events {
		if ev.AllDay || ev.Declined || ev.StartTime == "" || ev.UID == "" {
			continue
		}
		// Mark eligible BEFORE the parse-failure guard so a transient
		// calendarAlarmTime failure never reconcile-deletes an existing un-acted
		// calendar task for a UID still present in the feed.
		eligible[ev.UID] = true
		timeStr, dateStr, ok := calendarAlarmTime(ev.StartTime, now)
		if !ok {
			continue
		}

		idx := -1
		for i := range s.Tasks {
			if s.Tasks[i].CalUID == ev.UID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			changed := false
			if s.Tasks[idx].Time != timeStr {
				s.Tasks[idx].Time = timeStr
				changed = true
			}
			if s.Tasks[idx].Date != dateStr {
				s.Tasks[idx].Date = dateStr
				changed = true
			}
			if s.Tasks[idx].Desc != ev.Summary {
				s.Tasks[idx].Desc = ev.Summary
				changed = true
			}
			if changed {
				updated++
			}
			// Ack state (Done/Snoozed/Missed/Dismissed) is deliberately preserved.
			continue
		}
		s.Tasks = append(s.Tasks, Task{
			ID:         s.NextID,
			Time:       timeStr,
			Date:       dateStr,
			Desc:       ev.Summary,
			Recurrence: Once,
			CreatedAt:  now.Format(time.RFC3339),
			Source:     "calendar",
			CalUID:     ev.UID,
		})
		s.NextID++
		added++
	}

	// Reconcile vanished calendar tasks: drop un-acted ones, keep Done tombstones.
	var remaining []Task
	for _, t := range s.Tasks {
		if t.Source == "calendar" && t.CalUID != "" && !eligible[t.CalUID] {
			if !t.Done {
				dropped++
				continue
			}
		}
		remaining = append(remaining, t)
	}
	s.Tasks = remaining

	// Only touch disk when something actually changed. A steady-state refresh
	// (nothing added/updated/dropped) leaves s.Tasks byte-identical, so sorting
	// and rewriting tasks.json every 15-min tick is pure SD-card wear — an
	// explicit v2 constraint to avoid.
	if added+updated+dropped > 0 {
		s.sortTasks()
		s.saveLocked()
	}
	return added, updated, dropped
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
			// Copy on write: readers hold the old map after the lock drops.
			h := maps.Clone(s.Tasks[i].History)
			if h == nil {
				h = make(map[string]bool)
			}
			h[s.LastReset] = true
			s.Tasks[i].History = h
		}
	}
	// archive once-tasks that were created before today
	for i := range s.Tasks {
		if s.Tasks[i].Archived {
			continue
		}
		if s.Tasks[i].Recurrence == Once {
			// A dated once-task survives until its Date has passed (archived only
			// the day AFTER its Date); an undated one archives once its creation
			// day is behind us, as before.
			var archive bool
			if s.Tasks[i].Date != "" {
				archive = s.Tasks[i].Date < now.Format("2006-01-02")
			} else {
				archive = createdOnDate(s.Tasks[i]).Before(startOfDay(now))
			}
			if archive {
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
		s.Tasks[i].Missed = false
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
// daemon scheduler so firing can be evaluated deterministically). This is the
// single date-gate: a task carrying an explicit future/past Date never fires on
// any other day, so the scheduler (via escalationEligible) and buildStatus (via
// shouldFireToday) agree.
func shouldFireTodayAt(t Task, now time.Time) bool {
	if t.Archived {
		return false
	}
	// An explicit Date pins a ONCE task to exactly that day; empty = today.
	// Scoped to Once so a stray Date on a recurring task can never zombie it —
	// a dated non-Once task still fires on its recurrence, ignoring the Date.
	if t.Recurrence == Once && t.Date != "" && t.Date != now.Format("2006-01-02") {
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
