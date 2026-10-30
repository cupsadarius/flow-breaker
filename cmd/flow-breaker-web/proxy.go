package main

import (
	"io"
	"net/http"
	"regexp"
	"strings"
)

// proxyRoutes is the daemon API the phone may reach, by method; paths are
// relative to /api/.
var proxyRoutes = map[string]*regexp.Regexp{
	http.MethodGet:    regexp.MustCompile(`^(tasks|status|inbox|habits)$`),
	http.MethodPost:   regexp.MustCompile(`^(capture|task/\d+(/(done|undo|snooze|dismiss))?|inbox/\d+/schedule)$`),
	http.MethodDelete: regexp.MustCompile(`^inbox/\d+$`),
}

// proxyHandler forwards allowed /api/* calls to the Pi daemon and copies the
// answer back.
func proxyHandler(pi string, client *http.Client) http.Handler {
	pi = strings.TrimRight(pi, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/")
		re, ok := proxyRoutes[r.Method]
		if !ok || !re.MatchString(rest) {
			http.NotFound(w, r)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, pi+"/"+rest, http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "daemon unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	})
}
