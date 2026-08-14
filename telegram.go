package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// sendTelegram posts text to the bot's chat via the Telegram Bot API:
// POST {baseURL}/bot{token}/sendMessage with a JSON {chat_id, text} body. The
// caller supplies the *http.Client and baseURL so tests can point at an
// httptest.Server (baseURL defaults to https://api.telegram.org in production
// via telegramConfig). A 403 — Telegram's response when the user has never
// /start-ed the bot (or has blocked it) — yields a clear, actionable error;
// any other non-2xx yields an error carrying the status and response body.
func sendTelegram(client *http.Client, baseURL, token, chatID, text string) error {
	payload, err := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return err
	}
	url := strings.TrimRight(baseURL, "/") + "/bot" + token + "/sendMessage"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("telegram: bot can't message user (send /start to the bot once)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram send: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

// telegramConfig reads the Telegram fallback channel config from the
// environment. TELEGRAM_API_BASE defaults to https://api.telegram.org
// (overridable so tests can point at an httptest.Server); TELEGRAM_BOT_TOKEN
// and TELEGRAM_CHAT_ID have no defaults (empty = channel unconfigured).
func telegramConfig() (apiBase, token, chatID string) {
	apiBase = os.Getenv("TELEGRAM_API_BASE")
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	return apiBase, os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
}
