package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePi records every request it gets.
func fakePi(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Tailscale-User-Login", "me@example.com")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func testMux(t *testing.T, pi string) http.Handler {
	t.Helper()
	return newMux(config{pi: pi, users: "me@example.com", hosts: "example.com", data: t.TempDir()})
}

func TestProxyForwardsAllowed(t *testing.T) {
	pi, got := fakePi(t)
	h := testMux(t, pi.URL)
	if w := call(h, "GET", "/api/status", ""); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("GET status = %d %q", w.Code, w.Body.String())
	}
	if w := call(h, "POST", "/api/task/3/done", `{"source":"phone"}`); w.Code != 200 {
		t.Fatalf("POST done = %d", w.Code)
	}
	want := []string{"GET /status ", `POST /task/3/done {"source":"phone"}`}
	if strings.Join(*got, "|") != strings.Join(want, "|") {
		t.Fatalf("pi got %q, want %q", *got, want)
	}
}

func TestProxyRejectsOthers(t *testing.T) {
	pi, got := fakePi(t)
	h := testMux(t, pi.URL)
	for _, c := range [][2]string{{"GET", "/api/settings"}, {"POST", "/api/task/x/done"}, {"DELETE", "/api/task/3"}, {"GET", "/api/../status"}} {
		body := ""
		if c[0] != "GET" {
			body = "{}"
		}
		if w := call(h, c[0], c[1], body); w.Code == http.StatusOK {
			t.Errorf("%s %s = 200, want a refusal", c[0], c[1])
		}
	}
	if len(*got) != 0 {
		t.Fatalf("pi was called: %q", *got)
	}
}

func TestGuardNeedsUser(t *testing.T) {
	pi, _ := fakePi(t)
	h := testMux(t, pi.URL)
	r := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("no user: %d, want 403", w.Code)
	}
	r = httptest.NewRequest("GET", "/api/health", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health: %d, want 200", w.Code)
	}
}
