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
