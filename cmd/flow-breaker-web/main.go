// flow-breaker-web is the phone client: a PWA served from the Intel Mac that
// proxies the Pi daemon and turns ntfy alarms into Web Push notifications.
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed web
var webFS embed.FS

type config struct {
	addr, pi, users, hosts, data, subject string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	return config{
		addr:    env("WEB_ADDR", ":8091"),
		pi:      env("FLOW_BREAKER_URL", "http://localhost:7777"),
		users:   os.Getenv("WEB_ALLOWED_USERS"),
		hosts:   os.Getenv("WEB_ALLOWED_HOSTS"),
		data:    env("WEB_DATA", "/data"),
		subject: env("VAPID_SUBJECT", "mailto:admin@localhost"),
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
	p, err := newPusher(cfg.data, cfg.subject)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("flow-breaker-web on %s → %s", cfg.addr, cfg.pi)
	log.Fatal(http.ListenAndServe(cfg.addr, newMux(cfg, p)))
}
