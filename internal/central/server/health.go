package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// Pinger is the optional interface a Store can implement so
// /ready can verify the database is reachable. The in-memory
// MemoryStore does not implement it (no DB to ping); the
// PostgresStore does. /ready falls back to "ready" for stores
// that don't implement Pinger because there's nothing to be
// not-ready about.
type Pinger interface {
	Ping(ctx context.Context) error
}

// handleHealth implements HEALTH.1's /health endpoint.
//
// Liveness probe: returns 200 as long as the process is running
// and serving HTTP. Deliberately does no I/O — Kubernetes-style
// liveness should signal "the process is alive", not "the
// system is healthy". /ready handles the harder question.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET only")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = fmt.Fprintln(w, `{"status":"ok"}`)
}

// handleReady implements HEALTH.1's /ready endpoint.
//
// Readiness probe: 200 only when the database is reachable and
// migrations are applied. The schema migrator runs at server
// startup before the HTTP listener accepts traffic, so by the
// time this handler is callable migrations have either succeeded
// or NewPostgresStore returned an error and the binary exited;
// we don't recheck the migration table here — instead we Ping
// the pool to confirm connectivity hasn't dropped since startup.
//
// The in-memory store is always ready (no external dependency).
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET only")
		return
	}
	pinger, ok := s.store.(Pinger)
	if !ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = fmt.Fprintln(w, `{"status":"ready","db":"in-memory"}`)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := pinger.Ping(ctx); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"status":"not-ready","error":%q}`+"\n", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = fmt.Fprintln(w, `{"status":"ready","db":"reachable"}`)
}

// handleMetrics implements HEALTH.1's /metrics endpoint in the
// Prometheus exposition format. Snapshot is taken at request
// time; readers see a consistent view of every counter without
// a global mutex.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET only")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.metrics.Snapshot().WriteProm(w); err != nil {
		// At this point we've already started writing; can't
		// switch to a 5xx. Just stop and let the client see
		// what we wrote.
		return
	}
}

// recordEvent is the single-call site for the Append metric
// path. Keeping it next to the metrics struct makes the
// audit-vs-non-audit decision visible alongside the metric
// definitions.
func (s *Server) recordEvent(eventType string, added bool) {
	if s.metrics == nil {
		return
	}
	s.metrics.RecordEventAppended(added, audit.IsAuditEvent(eventType))
}
