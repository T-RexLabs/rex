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
func (a *PostgresAuditAppender) Append(ctx context.Context, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("server: marshal audit payload: %w", err)
	}
	a.mu.Lock()
	ts := a.clock.Now()
	a.mu.Unlock()
	rec := eventlog.Record{
		ID:        ts.String(),
		Type:      eventType,
		Version:   1,
		Timestamp: ts,
		Actor:     a.actor,
		Payload:   body,
	}
	if _, err := a.store.Append(ctx, rec); err != nil {
		return fmt.Errorf("server: append audit event %q: %w", eventType, err)
	}
	return nil
}
