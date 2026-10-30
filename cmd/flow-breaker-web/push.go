package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// pushEndpointOK limits subscriptions to the browser push services, so the
// server never posts to an address a client picked.
var pushEndpointOK = func(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	for _, h := range []string{"fcm.googleapis.com", "push.services.mozilla.com", "web.push.apple.com", "notify.windows.com"} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// pusher holds the VAPID keys and the one phone subscription, both as 0600
// files in dir.
type pusher struct {
	mu      sync.Mutex
	dir     string
	subject string
	keys    struct{ Public, Private string }
}

func newPusher(dir, subject string) (*pusher, error) {
	p := &pusher{dir: dir, subject: subject}
	path := filepath.Join(dir, "vapid.json")
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &p.keys); err != nil {
			return nil, fmt.Errorf("vapid.json: %w", err)
		}
		return p, nil
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, err
	}
	p.keys.Public, p.keys.Private = pub, priv
	return p, writeJSON0600(path, p.keys)
}

func writeJSON0600(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *pusher) PublicKey() string { return p.keys.Public }

func (p *pusher) subPath() string { return filepath.Join(p.dir, "push.json") }

func (p *pusher) Save(sub webpush.Subscription) error {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || !pushEndpointOK(u) {
		return errors.New("not a push service endpoint")
	}
	if sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return errors.New("subscription keys missing")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return writeJSON0600(p.subPath(), sub)
}

func (p *pusher) Delete() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.Remove(p.subPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Send pushes payload to the stored subscription. No subscription is not an
// error; a 404/410 from the push service deletes the subscription.
func (p *pusher) Send(payload []byte) error {
	p.mu.Lock()
	data, err := os.ReadFile(p.subPath())
	p.mu.Unlock()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var sub webpush.Subscription
	if err := json.Unmarshal(data, &sub); err != nil {
		return err
	}
	resp, err := webpush.SendNotification(payload, &sub, &webpush.Options{
		Subscriber:      p.subject,
		VAPIDPublicKey:  p.keys.Public,
		VAPIDPrivateKey: p.keys.Private,
		TTL:             15 * 60,
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return p.Delete()
	case resp.StatusCode >= 300:
		return fmt.Errorf("push service: %s", resp.Status)
	}
	return nil
}

// routes registers the /api/push/* endpoints.
func (p *pusher) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/push/key", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"public_key": p.PublicKey()})
	})
	mux.HandleFunc("PUT /api/push/subscription", func(w http.ResponseWriter, r *http.Request) {
		var sub webpush.Subscription
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&sub); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := p.Save(sub); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("DELETE /api/push/subscription", func(w http.ResponseWriter, r *http.Request) {
		if err := p.Delete(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/push/test", func(w http.ResponseWriter, r *http.Request) {
		if err := p.Send([]byte(`{"title":"flow-breaker","body":"Test notification","tag":"test"}`)); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
}
