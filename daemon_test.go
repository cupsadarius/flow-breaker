package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthRoute(t *testing.T) {
	srv := httptest.NewServer(newDaemonMux(&Store{}, newScheduler(&Store{})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", string(body), "ok")
	}
}

func TestDaemonAddrDefaultLoopback(t *testing.T) {
	t.Setenv("FLOW_BREAKER_ADDR", "")
	if got := daemonAddr(); got != "127.0.0.1:7777" {
		t.Fatalf("daemonAddr() = %q, want 127.0.0.1:7777 (loopback only)", got)
	}
}

func TestDaemonAddrEnvOverride(t *testing.T) {
	t.Setenv("FLOW_BREAKER_ADDR", "0.0.0.0:9999")
	if got := daemonAddr(); got != "0.0.0.0:9999" {
		t.Fatalf("daemonAddr() = %q, want the env override", got)
	}
}
