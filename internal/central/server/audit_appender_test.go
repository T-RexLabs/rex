package server

import (
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
