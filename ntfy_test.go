package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishNtfy(t *testing.T) {
	var gotPath, gotTitle, gotPriority, gotActions, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("X-Title")
		gotPriority = r.Header.Get("X-Priority")
		gotActions = r.Header.Get("X-Actions")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	actions := []NtfyAction{
		{Label: "Done", URL: "https://pi.example/task/42/done"},
		{Label: "Snooze", URL: "https://pi.example/task/42/snooze"},
	}
	err := publishNtfy(srv.Client(), srv.URL, "flow", "Standup", "time to standup", 4, actions)
	if err != nil {
		t.Fatalf("publishNtfy returned error: %v", err)
	}

	if gotPath != "/flow" {
		t.Errorf("path = %q, want /flow", gotPath)
	}
	if gotTitle != "Standup" {
		t.Errorf("X-Title = %q, want Standup", gotTitle)
	}
	if gotPriority != "4" {
		t.Errorf("X-Priority = %q, want 4", gotPriority)
	}
	if gotBody != "time to standup" {
		t.Errorf("body = %q, want %q", gotBody, "time to standup")
	}
	// Both actions present, each an http action with the right URL.
	if !strings.Contains(gotActions, "action=http") {
		t.Errorf("X-Actions missing http action: %q", gotActions)
	}
	if !strings.Contains(gotActions, "label=Done") || !strings.Contains(gotActions, "https://pi.example/task/42/done") {
		t.Errorf("X-Actions missing Done action: %q", gotActions)
	}
	if !strings.Contains(gotActions, "label=Snooze") || !strings.Contains(gotActions, "https://pi.example/task/42/snooze") {
		t.Errorf("X-Actions missing Snooze action: %q", gotActions)
	}
	if !strings.Contains(gotActions, "method=POST") {
		t.Errorf("X-Actions missing method=POST: %q", gotActions)
	}
}

func TestPublishNtfyNoActions(t *testing.T) {
	var gotActions string
	seen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		gotActions = r.Header.Get("X-Actions")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := publishNtfy(srv.Client(), srv.URL, "flow", "t", "b", 3, nil); err != nil {
		t.Fatalf("publishNtfy returned error: %v", err)
	}
	if !seen {
		t.Fatal("server never received request")
	}
	if gotActions != "" {
		t.Errorf("X-Actions should be empty with no actions, got %q", gotActions)
	}
}

func TestPublishNtfyNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	err := publishNtfy(srv.Client(), srv.URL, "flow", "t", "b", 3, nil)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include response body: %v", err)
	}
}
