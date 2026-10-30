package main

import "testing"

func TestParseAlarm(t *testing.T) {
	msg := ntfyMessage{Message: "09:30 — stretch break", Priority: 4}
	msg.Actions = append(msg.Actions, ntfyAction{Label: "Done", URL: "https://pi/task/42/done"})
	a, ok := parseAlarm(msg)
	if !ok || a.TaskID != 42 || a.Time != "09:30" || a.Desc != "stretch break" || a.Priority != 4 {
		t.Fatalf("alarm = %+v ok=%v", a, ok)
	}
	if _, ok := parseAlarm(ntfyMessage{Message: "hello"}); ok {
		t.Fatal("message without a Done action parsed as an alarm")
	}
}

func TestAlarmPayload(t *testing.T) {
	got := string(alarmPayload(alarm{TaskID: 42, Time: "09:30", Desc: "stretch break", Priority: 4}))
	want := `{"title":"flow-breaker","body":"09:30 — stretch break","taskId":42,"tag":"task-42","priority":4}`
	if got != want {
		t.Fatalf("payload = %s\nwant      %s", got, want)
	}
}
