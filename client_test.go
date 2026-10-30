package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonURLEnvWins(t *testing.T) {
	t.Setenv("FLOW_BREAKER_URL", "http://env.example:1234")
	// A client file that should be ignored because the env var wins.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "flow-breaker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client"), []byte("http://file.example:5678\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := daemonURL(); got != "http://env.example:1234" {
		t.Fatalf("daemonURL() = %q, want env value", got)
	}
}

func TestDaemonURLFileWinsWhenNoEnv(t *testing.T) {
	t.Setenv("FLOW_BREAKER_URL", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "flow-breaker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client"), []byte("  http://file.example:5678\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := daemonURL(); got != "http://file.example:5678" {
		t.Fatalf("daemonURL() = %q, want trimmed file value", got)
	}
}

func TestDaemonURLDefault(t *testing.T) {
	t.Setenv("FLOW_BREAKER_URL", "")
	home := t.TempDir() // empty: no client file present
	t.Setenv("HOME", home)
	if got := daemonURL(); got != "http://localhost:7777" {
		t.Fatalf("daemonURL() = %q, want default", got)
	}
}

func TestClientGetDecodesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"nudge": "hello"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, http: srv.Client()}
	var out struct {
		Nudge string `json:"nudge"`
	}
	if err := c.Get("/status", &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.Nudge != "hello" {
		t.Fatalf("Nudge = %q, want hello", out.Nudge)
	}
}

func TestClientGetDaemonDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now nothing is listening

	c := &Client{BaseURL: url, http: &http.Client{}}
	err := c.Get("/status", nil)
	if err == nil {
		t.Fatal("expected error against closed server, got nil")
	}
	if !errors.Is(err, ErrDaemonDown) {
		t.Fatalf("error = %v, want ErrDaemonDown", err)
	}
}

func TestClientNon2xxCarriesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": "task 99 not found"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, http: srv.Client()}
	err := c.Delete("/task/99", map[string]any{"source": "cli"}, nil)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if errors.Is(err, ErrDaemonDown) {
		t.Fatalf("404 should not be ErrDaemonDown: %v", err)
	}
	if got := err.Error(); got != "task 99 not found" {
		t.Fatalf("error = %q, want server message", got)
	}
}
