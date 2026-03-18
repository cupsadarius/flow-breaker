package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── iCal feed calendar integration ──────────────────────────────────────────

type CalendarEvent struct {
	ID           string `json:"id"`
	Summary      string `json:"summary"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	CalendarName string `json:"calendar_name"`
	AllDay       bool   `json:"all_day"`
}

type CalendarCache struct {
	Events    []CalendarEvent `json:"events"`
	FetchedAt string          `json:"fetched_at"`
	ForDate   string          `json:"for_date"`
}

type CalendarFeed struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

func feedsPath() string { return filepath.Join(dataDir(), "calendar_feeds.json") }
func cachePath() string { return filepath.Join(dataDir(), "calendar_cache.json") }

func loadFeeds() ([]CalendarFeed, error) {
	data, err := os.ReadFile(feedsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var feeds []CalendarFeed
	if err := json.Unmarshal(data, &feeds); err != nil {
		return nil, err
	}
	return feeds, nil
}

func saveFeeds(feeds []CalendarFeed) error {
	data, err := json.MarshalIndent(feeds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(feedsPath(), data, 0o600)
}

func addFeed(url, label string) error {
	feeds, err := loadFeeds()
	if err != nil {
		return err
	}
	for _, f := range feeds {
		if f.URL == url {
			return fmt.Errorf("feed already exists: %s", url)
		}
	}
	feeds = append(feeds, CalendarFeed{URL: url, Label: label})
	return saveFeeds(feeds)
}

func removeFeed(query string) error {
	feeds, err := loadFeeds()
	if err != nil {
		return err
	}
	q := strings.ToLower(query)
	for i, f := range feeds {
		if strings.ToLower(f.URL) == q || strings.Contains(strings.ToLower(f.Label), q) {
			feeds = append(feeds[:i], feeds[i+1:]...)
			return saveFeeds(feeds)
		}
	}
	return fmt.Errorf("no feed matching %q", query)
}

// ── iCal parser ─────────────────────────────────────────────────────────────

func fetchICalFeed(url string) (string, error) {
	// Local file path
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		path := strings.TrimPrefix(url, "file://")
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read file failed: %w", err)
		}
		return string(data), nil
	}

	// HTTP fetch
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d from feed", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}
	return string(body), nil
}

// unfoldICalLines handles RFC 5545 line unfolding: lines starting with space/tab
// are continuations of the previous line.
func unfoldICalLines(raw string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		// strip trailing \r
		line = strings.TrimRight(line, "\r")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if len(lines) > 0 {
				lines[len(lines)-1] += line[1:]
			}
		} else {
			lines = append(lines, line)
		}
	}
	return lines
}

// parseICalDateTime handles three datetime forms:
//   - 20260318T140000Z (UTC)
//   - TZID param + 20260318T140000 (with timezone)
//   - 20260318 (all-day, VALUE=DATE)
func parseICalDateTime(value, tzid string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)

	// All-day: just a date
	if len(value) == 8 {
		t, err := time.Parse("20060102", value)
		return t, true, err
	}

	// UTC form: ends with Z
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		if err != nil {
			return time.Time{}, false, err
		}
		return t.In(time.Now().Location()), false, nil
	}

	// With TZID
	if tzid != "" {
		loc, err := time.LoadLocation(tzid)
		if err != nil {
			// fallback to local time if timezone unknown
			loc = time.Now().Location()
		}
		t, err := time.ParseInLocation("20060102T150405", value, loc)
		if err != nil {
			return time.Time{}, false, err
		}
		return t.In(time.Now().Location()), false, nil
	}

	// No timezone info — assume local
	t, err := time.ParseInLocation("20060102T150405", value, time.Now().Location())
	return t, false, err
}

func parseICalEvents(ical string, label string) []CalendarEvent {
	lines := unfoldICalLines(ical)
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayEnd := todayStart.Add(24 * time.Hour)

	var events []CalendarEvent
	inEvent := false
	var uid, summary, dtStartRaw, dtStartTZID, dtEndRaw, dtEndTZID string
	var dtStartIsDate bool

	for _, line := range lines {
		if line == "BEGIN:VEVENT" {
			inEvent = true
			uid, summary = "", ""
			dtStartRaw, dtStartTZID, dtEndRaw, dtEndTZID = "", "", "", ""
			dtStartIsDate = false
			continue
		}
		if line == "END:VEVENT" {
			if inEvent && dtStartRaw != "" {
				startTime, allDay, err := parseICalDateTime(dtStartRaw, dtStartTZID)
				if err != nil {
					inEvent = false
					continue
				}

				// For all-day events: check if the date matches today
				if allDay || dtStartIsDate {
					startDate := time.Date(startTime.Year(), startTime.Month(), startTime.Day(), 0, 0, 0, 0, now.Location())
					endDate := startDate.Add(24 * time.Hour)
					if dtEndRaw != "" {
						et, _, err := parseICalDateTime(dtEndRaw, dtEndTZID)
						if err == nil {
							endDate = time.Date(et.Year(), et.Month(), et.Day(), 0, 0, 0, 0, now.Location())
						}
					}
					// Event overlaps today if startDate < todayEnd && endDate > todayStart
					if startDate.Before(todayEnd) && endDate.After(todayStart) {
						if summary == "" {
							summary = "(no title)"
						}
						events = append(events, CalendarEvent{
							ID:           uid,
							Summary:      summary,
							CalendarName: label,
							AllDay:       true,
						})
					}
				} else {
					// Timed event
					endTime := startTime.Add(time.Hour) // default 1h
					if dtEndRaw != "" {
						et, _, err := parseICalDateTime(dtEndRaw, dtEndTZID)
						if err == nil {
							endTime = et
						}
					}
					// Event overlaps today if start < todayEnd && end > todayStart
					if startTime.Before(todayEnd) && endTime.After(todayStart) {
						if summary == "" {
							summary = "(no title)"
						}
						events = append(events, CalendarEvent{
							ID:           uid,
							Summary:      summary,
							StartTime:    startTime.In(now.Location()).Format("15:04"),
							EndTime:      endTime.In(now.Location()).Format("15:04"),
							CalendarName: label,
						})
					}
				}
			}
			inEvent = false
			continue
		}
		if !inEvent {
			continue
		}

		// Parse property;params:value
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		propPart := line[:colonIdx]
		value := line[colonIdx+1:]

		// Split property name from parameters
		semiIdx := strings.Index(propPart, ";")
		propName := propPart
		params := ""
		if semiIdx >= 0 {
			propName = propPart[:semiIdx]
			params = propPart[semiIdx+1:]
		}

		switch propName {
		case "UID":
			uid = value
		case "SUMMARY":
			summary = value
		case "DTSTART":
			dtStartRaw = value
			dtStartTZID = extractParam(params, "TZID")
			if strings.Contains(params, "VALUE=DATE") {
				dtStartIsDate = true
			}
		case "DTEND":
			dtEndRaw = value
			dtEndTZID = extractParam(params, "TZID")
		}
	}

	return events
}

func extractParam(params, key string) string {
	for _, p := range strings.Split(params, ";") {
		if strings.HasPrefix(p, key+"=") {
			return p[len(key)+1:]
		}
	}
	return ""
}

// ── Fetch & cache ───────────────────────────────────────────────────────────

func fetchTodayEvents(feeds []CalendarFeed) ([]CalendarEvent, error) {
	var allEvents []CalendarEvent
	for _, feed := range feeds {
		ical, err := fetchICalFeed(feed.URL)
		if err != nil {
			return nil, fmt.Errorf("feed %q: %w", feed.Label, err)
		}
		events := parseICalEvents(ical, feed.Label)
		allEvents = append(allEvents, events...)
	}
	return allEvents, nil
}

func getCachedOrFetchEvents(feeds []CalendarFeed, cacheMins int) ([]CalendarEvent, error) {
	today := time.Now().Format("2006-01-02")

	// try cache
	data, err := os.ReadFile(cachePath())
	if err == nil {
		var cache CalendarCache
		if json.Unmarshal(data, &cache) == nil && cache.ForDate == today {
			fetched, err := time.Parse(time.RFC3339, cache.FetchedAt)
			if err == nil && time.Since(fetched) < time.Duration(cacheMins)*time.Minute {
				return cache.Events, nil
			}
		}
	}

	// fetch fresh
	events, err := fetchTodayEvents(feeds)
	if err != nil {
		return nil, err
	}

	// save cache
	cache := CalendarCache{
		Events:    events,
		FetchedAt: time.Now().Format(time.RFC3339),
		ForDate:   today,
	}
	cacheData, _ := json.MarshalIndent(cache, "", "  ")
	os.WriteFile(cachePath(), cacheData, 0o644)

	return events, nil
}
