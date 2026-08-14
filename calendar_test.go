package main

import (
	"testing"
	"time"
)

// TestParseICalUIDAndDeclined guards Task 1 + the self-scoped declined fix: the
// parser exposes the event UID, and Declined is set ONLY when the DECLINED
// partstat is on the user's own ATTENDEE line. A colleague's decline must not
// suppress the event, and with no self-email configured nothing is declined.
func TestParseICalUIDAndDeclined(t *testing.T) {
	today := time.Now().Format("20060102")
	// Event with two attendees: me (accepted) and a colleague (declined).
	ical := "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:standup-uid-123\r\n" +
		"SUMMARY:Standup\r\n" +
		"DTSTART:" + today + "T090000Z\r\n" +
		"DTEND:" + today + "T093000Z\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED;CN=Me:mailto:me@example.com\r\n" +
		"ATTENDEE;PARTSTAT=DECLINED;CN=Colleague:mailto:colleague@example.com\r\n" +
		"END:VEVENT\r\n" +
		// Event I personally declined.
		"BEGIN:VEVENT\r\n" +
		"UID:declined-uid-456\r\n" +
		"SUMMARY:Skip me\r\n" +
		"DTSTART:" + today + "T140000Z\r\n" +
		"DTEND:" + today + "T150000Z\r\n" +
		"ATTENDEE;PARTSTAT=DECLINED;CN=Me:mailto:me@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	parse := func() map[string]CalendarEvent {
		byUID := map[string]CalendarEvent{}
		for _, e := range parseICalEvents(ical, "work") {
			byUID[e.UID] = e
		}
		return byUID
	}

	// With self-email set: colleague's decline is ignored, my decline counts.
	t.Setenv("FLOW_BREAKER_CAL_EMAIL", "me@example.com")
	byUID := parse()
	stand, ok := byUID["standup-uid-123"]
	if !ok || stand.UID != "standup-uid-123" {
		t.Fatalf("standup UID not populated: %+v", byUID)
	}
	if stand.Declined {
		t.Fatal("a colleague's decline must NOT mark the event declined for me")
	}
	dec, ok := byUID["declined-uid-456"]
	if !ok {
		t.Fatalf("declined event missing from parse: %+v", byUID)
	}
	if !dec.Declined {
		t.Fatal("my own DECLINED partstat must set Declined")
	}

	// With NO self-email configured: never mark declined (better over-alarm).
	t.Setenv("FLOW_BREAKER_CAL_EMAIL", "")
	byUID = parse()
	if byUID["declined-uid-456"].Declined {
		t.Fatal("with no self-email, nothing may be marked declined")
	}
}
