package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendTelegram(t *testing.T) {
	var gotPath, gotContentType string
	var payload struct {
		ChatID any    `json:"chat_id"`
		Text   string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &payload)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	err := sendTelegram(context.Background(), srv.Client(), srv.URL, "TOKEN123", "999", "hello there")
	if err != nil {
		t.Fatalf("sendTelegram returned error: %v", err)
	}
	if gotPath != "/botTOKEN123/sendMessage" {
		t.Errorf("path = %q, want /botTOKEN123/sendMessage", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if payload.Text != "hello there" {
		t.Errorf("text = %q, want %q", payload.Text, "hello there")
	}
	if payload.ChatID == nil {
		t.Errorf("chat_id missing from payload")
	}
}

func TestSendTelegram403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
	}))
	defer srv.Close()

	err := sendTelegram(context.Background(), srv.Client(), srv.URL, "TOKEN", "999", "hi")
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if !strings.Contains(err.Error(), "/start") {
		t.Errorf("403 error should mention /start: %v", err)
	}
}

func TestSendTelegramNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	err := sendTelegram(context.Background(), srv.Client(), srv.URL, "TOKEN", "999", "hi")
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status 400: %v", err)
	}
}
