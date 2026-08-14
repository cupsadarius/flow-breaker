package main

import (
	"testing"
	"time"
)

// TestParseICalUIDAndDeclined guards Task 1: the parser must expose the event
// UID and set Declined from a best-effort PARTSTAT=DECLINED scan. Both events
// are dated today so they overlap the parser's today-only window.
func TestParseICalUIDAndDeclined(t *testing.T) {
	today := time.Now().Format("20060102")
	ical := "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:standup-uid-123\r\n" +
		"SUMMARY:Standup\r\n" +
		"DTSTART:" + today + "T090000Z\r\n" +
		"DTEND:" + today + "T093000Z\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:declined-uid-456\r\n" +
		"SUMMARY:Skip me\r\n" +
		"DTSTART:" + today + "T140000Z\r\n" +
		"DTEND:" + today + "T150000Z\r\n" +
		"ATTENDEE;PARTSTAT=DECLINED;CN=Me:mailto:me@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	events := parseICalEvents(ical, "work")

	byUID := map[string]CalendarEvent{}
	for _, e := range events {
		byUID[e.UID] = e
	}

	stand, ok := byUID["standup-uid-123"]
	if !ok {
		t.Fatalf("standup event missing; parsed %d events: %+v", len(events), events)
	}
	if stand.UID != "standup-uid-123" {
		t.Fatalf("UID not populated: %+v", stand)
	}
	if stand.Declined {
		t.Fatalf("standup must not be declined: %+v", stand)
	}

	dec, ok := byUID["declined-uid-456"]
	if !ok {
		t.Fatalf("declined event missing from parse: %+v", events)
	}
	if !dec.Declined {
		t.Fatalf("PARTSTAT=DECLINED event must set Declined: %+v", dec)
	}
}
