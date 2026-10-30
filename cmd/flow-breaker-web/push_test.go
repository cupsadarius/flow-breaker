package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func testSub(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	enc := base64.RawURLEncoding.EncodeToString
	return webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{P256dh: enc(k.PublicKey().Bytes()), Auth: enc(auth)}}
}

func TestPusherKeysPersist(t *testing.T) {
	dir := t.TempDir()
	a, err := newPusher(dir, "mailto:me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := newPusher(dir, "mailto:me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicKey() == "" || a.PublicKey() != b.PublicKey() {
		t.Fatalf("keys differ: %q vs %q", a.PublicKey(), b.PublicKey())
	}
	st, err := os.Stat(filepath.Join(dir, "vapid.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("vapid.json mode = %v, err %v; want 0600", st.Mode().Perm(), err)
	}
}

func TestSaveRejectsUnknownHost(t *testing.T) {
	p, _ := newPusher(t.TempDir(), "mailto:me@example.com")
	if err := p.Save(testSub(t, "https://evil.example.com/push")); err == nil {
		t.Fatal("saved a subscription on an unknown host")
	}
	if err := p.Save(testSub(t, "https://fcm.googleapis.com/fcm/send/abc")); err != nil {
		t.Fatalf("FCM endpoint rejected: %v", err)
	}
}

func TestSendGoneDeletesSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	prev := pushEndpointOK
	pushEndpointOK = func(*url.URL) bool { return true }
	defer func() { pushEndpointOK = prev }()

	dir := t.TempDir()
	p, _ := newPusher(dir, "mailto:me@example.com")
	if err := p.Save(testSub(t, srv.URL+"/push")); err != nil {
		t.Fatal(err)
	}
	p.Send([]byte(`{"title":"x"}`))
	if _, err := os.Stat(filepath.Join(dir, "push.json")); !os.IsNotExist(err) {
		t.Fatalf("subscription not deleted after 410 (err %v)", err)
	}
}

func TestSendTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	prevOK, prevTimeout := pushEndpointOK, pushTimeout
	pushEndpointOK = func(*url.URL) bool { return true }
	pushTimeout = 200 * time.Millisecond
	defer func() { pushEndpointOK, pushTimeout = prevOK, prevTimeout }()

	p, _ := newPusher(t.TempDir(), "mailto:me@example.com")
	p.Save(testSub(t, srv.URL+"/push"))
	done := make(chan error, 1)
	go func() { done <- p.Send([]byte(`{}`)) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hung push returned no error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send hung on a stalled push service")
	}
}

func TestNewPusherKeepsUnreadableKeys(t *testing.T) {
	dir := t.TempDir()
	if _, err := newPusher(dir, "mailto:me@example.com"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vapid.json")
	before, _ := os.ReadFile(path)
	os.Chmod(path, 0)
	defer os.Chmod(path, 0o600)
	if _, err := newPusher(dir, "mailto:me@example.com"); err == nil {
		t.Fatal("newPusher made new keys over an unreadable vapid.json")
	}
	os.Chmod(path, 0o600)
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("vapid.json was overwritten")
	}
}
