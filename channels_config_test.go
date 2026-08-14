package main

import "testing"

func TestNtfyConfigDefaults(t *testing.T) {
	t.Setenv("NTFY_URL", "")
	t.Setenv("NTFY_TOPIC", "")
	base, topic := ntfyConfig()
	if base != "" {
		t.Errorf("default NTFY_URL = %q, want empty", base)
	}
	if topic != "flow" {
		t.Errorf("default NTFY_TOPIC = %q, want flow", topic)
	}
}

func TestNtfyConfigOverride(t *testing.T) {
	t.Setenv("NTFY_URL", "http://127.0.0.1:8080")
	t.Setenv("NTFY_TOPIC", "alerts")
	base, topic := ntfyConfig()
	if base != "http://127.0.0.1:8080" {
		t.Errorf("NTFY_URL = %q", base)
	}
	if topic != "alerts" {
		t.Errorf("NTFY_TOPIC = %q", topic)
	}
}

func TestTelegramConfigDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_API_BASE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	base, token, chatID := telegramConfig()
	if base != "https://api.telegram.org" {
		t.Errorf("default TELEGRAM_API_BASE = %q", base)
	}
	if token != "" || chatID != "" {
		t.Errorf("token/chatID should be empty by default, got %q/%q", token, chatID)
	}
}

func TestTelegramConfigOverride(t *testing.T) {
	t.Setenv("TELEGRAM_API_BASE", "http://localhost:9000")
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("TELEGRAM_CHAT_ID", "42")
	base, token, chatID := telegramConfig()
	if base != "http://localhost:9000" {
		t.Errorf("TELEGRAM_API_BASE = %q", base)
	}
	if token != "tok" || chatID != "42" {
		t.Errorf("token/chatID = %q/%q", token, chatID)
	}
}

func TestPublicBaseURL(t *testing.T) {
	t.Setenv("FLOW_BREAKER_PUBLIC_URL", "")
	if got := publicBaseURL(); got != "" {
		t.Errorf("default publicBaseURL = %q, want empty", got)
	}
	t.Setenv("FLOW_BREAKER_PUBLIC_URL", "https://pi.tail.ts.net/")
	if got := publicBaseURL(); got != "https://pi.tail.ts.net" {
		t.Errorf("publicBaseURL = %q, want trailing slash trimmed", got)
	}
}
