package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrDaemonDown is returned when the daemon cannot be reached (dial refused,
// connection reset, or timeout). Callers test for it with errors.Is to render a
// friendly "daemon not running" message instead of a raw transport error.
var ErrDaemonDown = errors.New("flow-breaker daemon unreachable")

// daemonURL resolves the daemon base URL in priority order:
//  1. FLOW_BREAKER_URL env var
//  2. ~/.config/flow-breaker/client file (trimmed)
//  3. http://localhost:7777
func daemonURL() string {
	if u := os.Getenv("FLOW_BREAKER_URL"); u != "" {
		return u
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "flow-breaker", "client")
		if data, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				return s
			}
		}
	}
	return "http://localhost:7777"
}

// Client is a thin JSON HTTP client for the flow-breaker daemon.
type Client struct {
	BaseURL string
	http    *http.Client
}

// newClient builds a Client pointed at the resolved daemon URL with a 2s timeout.
func newClient() *Client { return newClientTimeout(2 * time.Second) }

// newClientTimeout builds a Client pointed at the resolved daemon URL with a
// caller-chosen timeout. The quiet/loud nudge path uses a short (800ms) timeout
// so a slow or down daemon can never block the agent's turn.
func newClientTimeout(d time.Duration) *Client {
	return &Client{
		BaseURL: daemonURL(),
		http:    &http.Client{Timeout: d},
	}
}

// do performs the request. body (if non-nil) is JSON-encoded; out (if non-nil)
// receives the decoded JSON response. Transport failures map to ErrDaemonDown;
// non-2xx responses return an error carrying the server's {"error"} message.
func (c *Client) do(method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Dial refused, connection reset, or timeout: the daemon is unreachable.
		return fmt.Errorf("%w: %v", ErrDaemonDown, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		var er struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &er)
		msg := er.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Get performs a GET and decodes the JSON response into out (nil out ignores it).
func (c *Client) Get(path string, out any) error { return c.do(http.MethodGet, path, nil, out) }

// GetText performs a GET and returns the raw response body as a string, for the
// plain-text /nudge endpoint. Transport failures (dial refused, reset, timeout)
// map to ErrDaemonDown; a non-2xx status returns the body text as an error.
func (c *Client) GetText(path string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDaemonDown, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return "", errors.New(msg)
	}
	return string(data), nil
}

// Post performs a POST with a JSON body and decodes the response into out.
func (c *Client) Post(path string, body, out any) error {
	return c.do(http.MethodPost, path, body, out)
}

// Delete performs a DELETE with a JSON body and decodes the response into out.
func (c *Client) Delete(path string, body, out any) error {
	return c.do(http.MethodDelete, path, body, out)
}
