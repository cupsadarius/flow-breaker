package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// NtfyAction is a single ntfy action button. Each button, when tapped in the
// phone's ntfy app, fires an HTTP POST to URL (used by 6B to build the
// task-done / task-snooze callbacks against the daemon's public base).
type NtfyAction struct {
	Label string // "Done" / "Snooze"
	URL   string // full POST target, e.g. https://<pi>/task/42/done
}

// publishNtfy posts a message to {baseURL}/{topic} using ntfy's HTTP publish
// API: the message is the request body, and metadata rides on headers —
// X-Title, X-Priority, and X-Actions (one http action per button, joined
// with ';'). priority follows ntfy's 1–5 contract; the zero value is treated as
// "unset" and no X-Priority header is sent (ntfy then applies its default 3).
// The caller supplies the *http.Client and baseURL so tests can point at an
// httptest.Server (no global client, no real network). ctx carries the
// per-attempt deadline (6B sets it). A non-2xx response yields an error carrying
// the status and (bounded) response body.
func publishNtfy(ctx context.Context, client *http.Client, baseURL, topic, title, body string, priority int, actions []NtfyAction) error {
	url := strings.TrimRight(baseURL, "/") + "/" + topic
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	if title != "" {
		req.Header.Set("X-Title", title)
	}
	// ntfy's valid priority range is 1–5; a 0 (int zero value) is invalid, so
	// omit the header entirely and let ntfy apply its default.
	if priority != 0 {
		req.Header.Set("X-Priority", strconv.Itoa(priority))
	}
	if len(actions) > 0 {
		req.Header.Set("X-Actions", encodeNtfyActions(actions))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy publish: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("ntfy publish: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	drainClose(resp)
	return nil
}

// encodeNtfyActions renders each action as an ntfy "http" action that POSTs to
// its URL, joining multiple actions with ';'. Uses ntfy's short header format,
// which is comma/semicolon-delimited: action URLs must contain neither ',' nor
// ';'. The fixed .../task/{id}/done|snooze targets are safe; if a query string
// carrying ',' or ';' is ever added, switch to ntfy's JSON action format.
func encodeNtfyActions(actions []NtfyAction) string {
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		parts = append(parts, fmt.Sprintf("action=http, label=%s, url=%s, method=POST", a.Label, a.URL))
	}
	return strings.Join(parts, "; ")
}

// drainClose drains any unread response body before closing it, so Go's HTTP
// transport can reuse the keep-alive connection instead of discarding it. Used
// on the success path where the body is otherwise ignored.
func drainClose(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// ntfyConfig reads the ntfy server base URL and topic from the environment.
// NTFY_URL has no default (empty = ntfy channel unconfigured); NTFY_TOPIC
// defaults to "flow".
func ntfyConfig() (baseURL, topic string) {
	baseURL = os.Getenv("NTFY_URL")
	topic = os.Getenv("NTFY_TOPIC")
	if topic == "" {
		topic = "flow"
	}
	return baseURL, topic
}

// publicBaseURL returns the daemon's externally reachable base URL
// (FLOW_BREAKER_PUBLIC_URL), trailing slash trimmed. 6B builds ntfy action
// callback targets like {public}/task/{id}/done on top of it. Empty when unset.
func publicBaseURL() string {
	return strings.TrimRight(os.Getenv("FLOW_BREAKER_PUBLIC_URL"), "/")
}
