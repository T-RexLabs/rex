package server

import (
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/identity"
)

// TestPostgresAuditAppenderWritesEvent covers the happy path:
// an Append lands a synthetic eventlog record under the
// request's org, with the actor + event-type set. The audit
// catalog gate ensures only registered types pass through (a
// guard against typos in the event-type string).
func TestPostgresAuditAppenderWritesEvent(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)

	actor := identity.Actor{Role: identity.RoleCentral, Fingerprint: identity.Fingerprint{}}
	appender := NewPostgresAuditAppender(store, actor)

	payload := audit.OrgMemberRoleChangedEvent{
		OrgID:       OrgIDFromContext(ctx),
		Fingerprint: "fp-bob",
		FromRole:    "member",
		ToRole:      "admin",
		ChangedBy:   "fp-alice",
	}
	if err := appender.Append(ctx, audit.EventTypeOrgMemberRoleChanged, payload); err != nil {
		t.Fatalf("Append: %v", err)
	}

	records, err := store.Since(ctx, "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	var saw bool
	for _, r := range records {
		if r.Type == audit.EventTypeOrgMemberRoleChanged {
			saw = true
			if r.Actor != actor.String() {
				t.Errorf("actor: got %q want %q", r.Actor, actor.String())
			}
		}
	}
	if !saw {
		t.Error("audit event not appended to store")
	}
}

// TestPostgresStoreSinceIsOrgScoped covers the read side that
// the /orgs/<id>/audit page relies on: events appended under one
// org are NOT returned by Since(WithOrgID(ctx, other-org)). This
// is the structural guarantee behind the org-audit projection;
// without it the page would leak cross-tenant audit rows.
func TestPostgresStoreSinceIsOrgScoped(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)

	// Create a second org alongside the seeded default.
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO orgs (name) VALUES ('other')`); err != nil {
		t.Fatalf("seed second org: %v", err)
	}
	defOrg, _ := store.LookupOrg(t.Context(), DefaultOrgName)
	otherOrg, _ := store.LookupOrg(t.Context(), "other")

	actor := identity.Actor{Role: identity.RoleCentral, Fingerprint: identity.Fingerprint{}}
	appender := NewPostgresAuditAppender(store, actor)

	// One event in each org.
	for orgID, who := range map[string]string{defOrg.ID: "fp-default", otherOrg.ID: "fp-other"} {
		ctx := WithOrgID(t.Context(), orgID)
		if err := appender.Append(ctx, audit.EventTypeOrgMemberRoleChanged, audit.OrgMemberRoleChangedEvent{
			OrgID:       orgID,
			Fingerprint: who,
			FromRole:    "member",
			ToRole:      "admin",
			ChangedBy:   "fp-alice",
		}); err != nil {
			t.Fatalf("append %s: %v", orgID, err)
		}
	}

	// Read back under the default org; the other org's row must
	// not leak through.
	rows, err := store.Since(WithOrgID(t.Context(), defOrg.ID), "")
	if err != nil {
		t.Fatalf("Since default: %v", err)
	}
	for _, r := range rows {
		if !strings.Contains(string(r.Payload), "fp-default") {
			t.Errorf("default org Since returned a non-default row: %s", r.Payload)
		}
	}
	// And vice versa.
	rows, err = store.Since(WithOrgID(t.Context(), otherOrg.ID), "")
	if err != nil {
		t.Fatalf("Since other: %v", err)
	}
	for _, r := range rows {
		if !strings.Contains(string(r.Payload), "fp-other") {
			t.Errorf("other org Since returned a non-other row: %s", r.Payload)
		}
	}
}

// TestPostgresAuditAppenderAppendManyBatchedWrite covers the
// bulk path: a single AppendMany call lands every event in one
// underlying store transaction. Mirrors how a future caller that
// fans out (bulk member changes, multi-amendment import) would
// drive the appender — without this path each emission would
// open its own tx and the wins from Store.AppendBatch would be
// lost in the audit layer.
func TestPostgresAuditAppenderAppendManyBatchedWrite(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	actor := identity.Actor{Role: identity.RoleCentral, Fingerprint: identity.Fingerprint{}}
	appender := NewPostgresAuditAppender(store, actor)
	orgID := OrgIDFromContext(ctx)

	events := []AuditEvent{
		{Type: audit.EventTypeOrgMemberRoleChanged, Payload: audit.OrgMemberRoleChangedEvent{
			OrgID: orgID, Fingerprint: "fp-1", FromRole: "member", ToRole: "admin", ChangedBy: "fp-alice",
		}},
		{Type: audit.EventTypeOrgMemberRoleChanged, Payload: audit.OrgMemberRoleChangedEvent{
			OrgID: orgID, Fingerprint: "fp-2", FromRole: "member", ToRole: "admin", ChangedBy: "fp-alice",
		}},
		{Type: audit.EventTypeOrgMemberRoleChanged, Payload: audit.OrgMemberRoleChangedEvent{
			OrgID: orgID, Fingerprint: "fp-3", FromRole: "admin", ToRole: "member", ChangedBy: "fp-alice",
		}},
	}
	if err := appender.AppendMany(ctx, events); err != nil {
		t.Fatalf("AppendMany: %v", err)
	}

	got, err := store.Since(ctx, "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	var seen int
	for _, r := range got {
		if r.Type == audit.EventTypeOrgMemberRoleChanged {
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("audit rows: got %d want 3", seen)
	}
}

// TestPostgresAuditAppenderAppendManyEmptyIsNoop confirms an
// empty AppendMany returns nil + writes nothing — callers that
// build the batch dynamically don't need to guard the call site.
func TestPostgresAuditAppenderAppendManyEmptyIsNoop(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	actor := identity.Actor{Role: identity.RoleCentral, Fingerprint: identity.Fingerprint{}}
	appender := NewPostgresAuditAppender(store, actor)
	if err := appender.AppendMany(ctx, nil); err != nil {
		t.Errorf("nil: %v", err)
	}
	if err := appender.AppendMany(ctx, []AuditEvent{}); err != nil {
		t.Errorf("empty slice: %v", err)
	}
	n, _ := store.Len(ctx)
	if n != 0 {
		t.Fatalf("len after empty AppendMany: got %d want 0", n)
	}
}
