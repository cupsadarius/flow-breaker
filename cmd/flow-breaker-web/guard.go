package main

import (
	"net/http"
	"net/url"
	"strings"
)

// Access rules, same as Body Ledger's server/src/health/web/security.py:
//
//   - Host (port stripped) must be localhost, 127.0.0.1 or in WEB_ALLOWED_HOSTS: else 421.
//   - Every path except /api/health needs Tailscale-User-Login in WEB_ALLOWED_USERS: else 403.
//     `tailscale serve` sets this header and strips client-sent copies.
//     Known limit: containers on the same compose network can reach this server directly and fake it.
//   - Unsafe methods need Content-Type application/json and, when an Origin is sent, the same
//     host:port as Host: else 403.
const csp = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"

type guard struct {
	users map[string]bool
	hosts map[string]bool
}

func newGuard(users, hosts string) guard {
	h := splitList(hosts)
	h["localhost"] = true
	h["127.0.0.1"] = true
	return guard{users: splitList(users), hosts: h}
}

// splitList parses a comma-separated env value into a lower-case set.
func splitList(s string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out[v] = true
		}
	}
	return out
}

// hostname returns the Host header without the port.
func hostname(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i >= 0 {
			return host[:i+1]
		}
		return host
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

func (g guard) deny(r *http.Request) (int, string) {
	if !g.hosts[hostname(r.Host)] {
		return http.StatusMisdirectedRequest, "unknown host"
	}
	if r.URL.Path != "/api/health" {
		if !g.users[strings.ToLower(strings.TrimSpace(r.Header.Get("Tailscale-User-Login")))] {
			return http.StatusForbidden, "not allowed"
		}
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return 0, ""
	}
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.ToLower(strings.TrimSpace(ct)) != "application/json" {
		return http.StatusForbidden, "writes must be application/json"
	}
	if origin, ok := r.Header["Origin"]; ok {
		u, err := url.Parse(origin[0])
		if err != nil || strings.ToLower(u.Host) != strings.ToLower(strings.TrimSpace(r.Host)) {
			return http.StatusForbidden, "cross-origin write"
		}
	}
	return 0, ""
}

// wrap sets the security headers on every response and rejects denied requests.
func (g guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if code, msg := g.deny(r); code != 0 {
			http.Error(w, msg, code)
			return
		}
		next.ServeHTTP(w, r)
	})
}
