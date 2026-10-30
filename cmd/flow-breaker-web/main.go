// flow-breaker-web is the phone client: a PWA served from the Intel Mac that
// proxies the Pi daemon and turns ntfy alarms into Web Push notifications.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

type config struct {
	addr, pi, users, hosts, data, subject string
	ntfyURL, ntfyTopic, ntfyToken         string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	return config{
		addr:      env("WEB_ADDR", ":8091"),
		pi:        env("FLOW_BREAKER_URL", "http://localhost:7777"),
		users:     os.Getenv("WEB_ALLOWED_USERS"),
		hosts:     os.Getenv("WEB_ALLOWED_HOSTS"),
		data:      env("WEB_DATA", "/data"),
		subject:   env("VAPID_SUBJECT", "mailto:admin@localhost"),
		ntfyURL:   os.Getenv("NTFY_URL"),
		ntfyTopic: env("NTFY_TOPIC", "flow"),
		ntfyToken: os.Getenv("NTFY_TOKEN"),
	}
}

func newMux(cfg config, p *pusher) http.Handler {
	mux := http.NewServeMux()
	p.routes(mux)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("/api/", proxyHandler(cfg.pi, &http.Client{Timeout: 15 * time.Second}))
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServerFS(static))
	return newGuard(cfg.users, cfg.hosts).wrap(mux)
}

func main() {
	cfg := loadConfig()
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		_, port, _ := strings.Cut(cfg.addr, ":")
		resp, err := http.Get("http://127.0.0.1:" + port + "/api/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	p, err := newPusher(cfg.data, cfg.subject)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.ntfyURL != "" {
		sub := &ntfySub{base: cfg.ntfyURL, topic: cfg.ntfyTopic, token: cfg.ntfyToken, push: p.Send}
		go sub.run(context.Background())
	} else {
		log.Printf("NTFY_URL unset: no alarm notifications")
	}
	log.Printf("flow-breaker-web on %s → %s", cfg.addr, cfg.pi)
	log.Fatal(http.ListenAndServe(cfg.addr, newMux(cfg, p)))
}
