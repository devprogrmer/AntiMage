package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// The metrics endpoint deliberately avoids node RPCs. Prometheus scrapes must
// not add another high-frequency accounting or quota polling loop.
func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if s.prometheusToken == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	const prefix = "Bearer "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) ||
		subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(authorization, prefix)), []byte(s.prometheusToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "Could not validate credentials")
		return
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	databaseUp := 0
	if s.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		if s.db.PingContext(ctx) == nil {
			databaseUp = 1
		}
		cancel()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "# HELP antimage_up AntiMage panel process is serving metrics.\n# TYPE antimage_up gauge\nantimage_up 1\n"+
		"# HELP antimage_database_up AntiMage database responds to a ping.\n# TYPE antimage_database_up gauge\nantimage_database_up %d\n"+
		"# HELP antimage_go_goroutines Number of goroutines in the panel process.\n# TYPE antimage_go_goroutines gauge\nantimage_go_goroutines %d\n"+
		"# HELP antimage_go_heap_alloc_bytes Go heap bytes allocated by the panel process.\n# TYPE antimage_go_heap_alloc_bytes gauge\nantimage_go_heap_alloc_bytes %d\n",
		databaseUp, runtime.NumGoroutine(), memory.HeapAlloc)
}
