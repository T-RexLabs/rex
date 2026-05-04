package server

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics is the central node's in-process metric registry.
// v1 ships the org-independent subset (HEALTH.2 amended via the
// 2026-05-04 amendment): events appended, audit-class events,
// duplicate appends, and active sessions. The org-scoped metrics
// named in HEALTH.2 (RBAC-deny-rate, per-org row counts) land
// with the tenant-routing task.
//
// All counters are uint64 monotonics manipulated via sync/atomic;
// the active-sessions gauge is a uint64 incremented and
// decremented by auth callbacks.
//
// Prometheus best practice for "X per second" is to expose the
// raw counter and let consumers compute rate(). So
// `sync_events_per_second` and `audit_write_rate` from the spec
// surface as `rex_central_events_appended_total` and
// `rex_central_audit_events_total` counters; consumers run
// `rate(rex_central_events_appended_total[1m])` to get the
// per-second rate.
type Metrics struct {
	// Counters
	eventsAppended  atomic.Uint64
	eventsDuplicate atomic.Uint64
	auditEvents     atomic.Uint64
	pushRequests    atomic.Uint64
	pushConflicts   atomic.Uint64
	authChallenges  atomic.Uint64

	// processStart is captured at construction so /metrics can
	// expose process_uptime_seconds. Useful sanity check that
	// sets every counter rate against a known elapsed time.
	processStart time.Time

	// activeSessions is sampled at /metrics scrape time from the
	// authState rather than maintained as an atomic gauge. The
	// scrape-time-sample model is drift-free even if a session
	// expires silently (no /logout endpoint to decrement on).
	activeSessions func() int
}

// NewMetrics returns a zero-valued Metrics whose active-sessions
// gauge always reads 0 until SetActiveSessionsSource is called
// to attach a callback that returns the current count.
func NewMetrics() *Metrics {
	return &Metrics{
		processStart:   time.Now(),
		activeSessions: func() int { return 0 },
	}
}

// SetActiveSessionsSource attaches the callback /metrics calls at
// scrape time to fill the active-sessions gauge. Server uses this
// to bind the auth state's activeSessions() at construction.
func (m *Metrics) SetActiveSessionsSource(f func() int) {
	if m == nil || f == nil {
		return
	}
	m.activeSessions = f
}

// Recording helpers — single-call sites in the request handlers
// keep the metrics package self-contained instead of leaking
// atomic.Uint64 across the codebase.

// RecordEventAppended is called on every successful Append (added
// or not). isAudit selects whether the audit-class counter also
// ticks.
func (m *Metrics) RecordEventAppended(added, isAudit bool) {
	if m == nil {
		return
	}
	if added {
		m.eventsAppended.Add(1)
		if isAudit {
			m.auditEvents.Add(1)
		}
	} else {
		m.eventsDuplicate.Add(1)
	}
}

// RecordPushRequest ticks once per /sync/events POST that
// reached the handler (post-auth). RecordPushConflict ticks when
// the same request returned 409 (since-mismatch). Together they
// give a divergence-rate metric.
func (m *Metrics) RecordPushRequest()  { if m != nil { m.pushRequests.Add(1) } }
func (m *Metrics) RecordPushConflict() { if m != nil { m.pushConflicts.Add(1) } }

// RecordAuthChallenge ticks once per challenge issued.
func (m *Metrics) RecordAuthChallenge() { if m != nil { m.authChallenges.Add(1) } }

// Snapshot returns the current values. Used by /metrics and
// (rarely) by tests asserting recording behavior. The
// active-sessions gauge is read from the configured source
// callback so it always reflects the live count, not an
// increment-decrement-paired counter.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	return MetricsSnapshot{
		EventsAppended:  m.eventsAppended.Load(),
		EventsDuplicate: m.eventsDuplicate.Load(),
		AuditEvents:     m.auditEvents.Load(),
		PushRequests:    m.pushRequests.Load(),
		PushConflicts:   m.pushConflicts.Load(),
		AuthChallenges:  m.authChallenges.Load(),
		ActiveSessions:  int64(m.activeSessions()),
		UptimeSeconds:   time.Since(m.processStart).Seconds(),
	}
}

// MetricsSnapshot is the immutable view of Metrics at one point
// in time. Used by /metrics and by tests.
type MetricsSnapshot struct {
	EventsAppended  uint64
	EventsDuplicate uint64
	AuditEvents     uint64
	PushRequests    uint64
	PushConflicts   uint64
	AuthChallenges  uint64
	ActiveSessions  int64
	UptimeSeconds   float64
}

// WriteProm emits a Snapshot in the Prometheus exposition
// format (text version). Intentionally hand-rolled — the format
// is tiny and adding a heavy client_golang dep for one endpoint
// is overkill (overview.ENG.1 favors keeping deps minimal).
func (s MetricsSnapshot) WriteProm(w io.Writer) error {
	for _, e := range []promMetric{
		{
			name:  "rex_central_events_appended_total",
			help:  "Events appended to the central event store (excludes duplicates).",
			kind:  "counter",
			value: float64(s.EventsAppended),
		},
		{
			name:  "rex_central_events_duplicate_total",
			help:  "Push events the central rejected as duplicate ids (sync.API.6 idempotency).",
			kind:  "counter",
			value: float64(s.EventsDuplicate),
		},
		{
			name:  "rex_central_audit_events_total",
			help:  "Subset of rex_central_events_appended_total whose type is audit-class (audit.IsAuditEvent).",
			kind:  "counter",
			value: float64(s.AuditEvents),
		},
		{
			name:  "rex_central_push_requests_total",
			help:  "Authenticated /sync/events POST requests reaching the handler.",
			kind:  "counter",
			value: float64(s.PushRequests),
		},
		{
			name:  "rex_central_push_conflicts_total",
			help:  "Push requests that returned 409 because the client's since cursor did not match HEAD.",
			kind:  "counter",
			value: float64(s.PushConflicts),
		},
		{
			name:  "rex_central_auth_challenges_total",
			help:  "Auth challenges issued via /sync/auth/challenge.",
			kind:  "counter",
			value: float64(s.AuthChallenges),
		},
		{
			name:  "rex_central_active_sessions",
			help:  "Currently authorized session tokens (gauge).",
			kind:  "gauge",
			value: float64(s.ActiveSessions),
		},
		{
			name:  "rex_central_process_uptime_seconds",
			help:  "Seconds since the central process started.",
			kind:  "gauge",
			value: s.UptimeSeconds,
		},
	} {
		if err := e.write(w); err != nil {
			return err
		}
	}
	return nil
}

type promMetric struct {
	name, help, kind string
	value            float64
}

func (m promMetric) write(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %g\n",
		m.name, m.help, m.name, m.kind, m.name, m.value,
	); err != nil {
		return err
	}
	return nil
}
