package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// PostgresAuditAppender satisfies AuthAuditAppender (and the
// web shell's analogous audit hook) by synthesising eventlog
// records and appending them to the central's PostgresStore.
//
// The central node had no audit producer before this — auth.go
// emits via the same hook but the appender was always nil in
// the production wireup. cmd/rex-central now constructs one and
// passes it as opts.AuthAudit so auth + token + org-admin
// mutations all land in the audit log under
// (actor=central, workspace_id="") so subsequent reads can
// pivot on event_type alone.
type PostgresAuditAppender struct {
	store *PostgresStore
	// actor is the central node's actor string (role=central +
	// fingerprint). Used as the source actor on every emitted
	// record so audit readers can distinguish central-originated
	// events from local-pushed ones.
	actor string
	// orgFn returns the org id the event should land under.
	// org-admin mutations carry their org_id through the call
	// chain; auth mutations stamp the central's default org
	// (matching what auth.go's existing ctx scoping does).
	clock *eventlog.Clock
	mu    sync.Mutex
}

// NewPostgresAuditAppender returns an appender bound to store.
// actor identifies who is appending — typically the central
// node's identity.Actor.String() form. The clock is an internal
// HLC source so events have monotonic timestamps independent of
// per-tx context.
func NewPostgresAuditAppender(store *PostgresStore, actor identity.Actor) *PostgresAuditAppender {
	return &PostgresAuditAppender{
		store: store,
		actor: actor.String(),
		clock: eventlog.NewClock(),
	}
}

// Append writes a synthetic eventlog.Record to the underlying
// PostgresStore. The record's ID is freshly generated from the
// internal HLC; WorkspaceID is left empty for org-scoped events
// (the events table's column is NOT NULL DEFAULT ” so this is
// fine). Org context still rides through the request ctx via
// WithOrgID — PostgresStore.Append enforces it.
//
// Best-effort: the appender's callers (auth handlers, org-admin
// adapters) treat failure as a log line, not a request error.
// Audit correctness must not gate user-visible operations.
//
// Implemented on top of AppendMany so the single-record path and
// the bulk path share one tx shape — that way any future caller
// that fans out (bulk member changes, multi-amendment imports,
// audit replay) inherits the same round-trip amortisation the
// push handler now gets from Store.AppendBatch.
func (a *PostgresAuditAppender) Append(ctx context.Context, eventType string, payload any) error {
	return a.AppendMany(ctx, []AuditEvent{{Type: eventType, Payload: payload}})
}

// AuditEvent is one entry in a batched audit emission. Type
// names the event class (must satisfy audit.IsAuditEvent for the
// metric tick to fire). Payload is marshalled once per event.
type AuditEvent struct {
	Type    string
	Payload any
}

// AppendMany synthesises one eventlog.Record per AuditEvent and
// hands the whole slice to the underlying Store.AppendBatch.
// IDs are stamped from the appender's internal HLC under the
// same mutex Append uses, so monotonicity holds across mixed
// single + bulk callers. Empty input is a no-op (no error,
// nothing emitted).
//
// Failure semantics match Append: best-effort. If the batch
// fails the caller logs and continues — audit must never gate a
// user-visible mutation. On partial failure the all-or-nothing
// tx guarantee means no records land, so a retry on the same
// batch is idempotent.
func (a *PostgresAuditAppender) AppendMany(ctx context.Context, events []AuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	recs := make([]eventlog.Record, len(events))
	a.mu.Lock()
	for i, ev := range events {
		body, err := json.Marshal(ev.Payload)
		if err != nil {
			a.mu.Unlock()
			return fmt.Errorf("server: marshal audit payload %q: %w", ev.Type, err)
		}
		ts := a.clock.Now()
		recs[i] = eventlog.Record{
			ID:        ts.String(),
			Type:      ev.Type,
			Version:   1,
			Timestamp: ts,
			Actor:     a.actor,
			Payload:   body,
		}
	}
	a.mu.Unlock()
	if _, err := a.store.AppendBatch(ctx, recs); err != nil {
		return fmt.Errorf("server: append audit batch (%d event(s)): %w", len(recs), err)
	}
	return nil
}
