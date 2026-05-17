package server

import (
	"context"
	"errors"
	"testing"
)

// seedMembership inserts a single membership row directly into
// the per-test schema. Mirrors what a future invite-redeem flow
// would do; the mutator tests need a pre-existing row to mutate.
func seedMembership(t *testing.T, s *PostgresStore, orgName, fingerprint, role string) {
	t.Helper()
	ctx := context.Background()
	org, err := s.LookupOrg(ctx, orgName)
	if err != nil {
		t.Fatalf("LookupOrg %q: %v", orgName, err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO org_memberships (org_id, fingerprint, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, fingerprint) DO UPDATE SET role = EXCLUDED.role
	`, org.ID, fingerprint, role); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// TestChangeMemberRolePromotesAndReturnsPrior covers the happy
// path: a member row exists; ChangeMemberRole flips the role and
// returns the prior value the audit layer needs.
func TestChangeMemberRolePromotesAndReturnsPrior(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	seedMembership(t, store, DefaultOrgName, "fp-bob", "member")

	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	prior, err := store.ChangeMemberRole(ctx, org.ID, "fp-bob", "admin")
	if err != nil {
		t.Fatalf("ChangeMemberRole: %v", err)
	}
	if prior != "member" {
		t.Errorf("prior: got %q want member", prior)
	}
	now, err := store.RoleFor(context.Background(), org.ID, "fp-bob")
	if err != nil {
		t.Fatalf("RoleFor: %v", err)
	}
	if now != "admin" {
		t.Errorf("new role: got %q want admin", now)
	}
}

// TestChangeMemberRoleIsNoOpWhenSame returns the prior role
// unchanged when newRole equals the current role; the audit
// emitter can use prior == newRole to skip the audit write.
func TestChangeMemberRoleIsNoOpWhenSame(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	seedMembership(t, store, DefaultOrgName, "fp-bob", "viewer")
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	prior, err := store.ChangeMemberRole(ctx, org.ID, "fp-bob", "viewer")
	if err != nil {
		t.Fatalf("ChangeMemberRole: %v", err)
	}
	if prior != "viewer" {
		t.Errorf("prior: got %q want viewer", prior)
	}
}

// TestChangeMemberRoleUnknownMembershipIsErrUnknownMembership
// covers the not-found path: the SQL UPDATE finds nothing and
// the sentinel surfaces so callers can 404.
func TestChangeMemberRoleUnknownMembershipIsErrUnknownMembership(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	_, err := store.ChangeMemberRole(ctx, org.ID, "fp-ghost", "admin")
	if !errors.Is(err, ErrUnknownMembership) {
		t.Fatalf("err: got %v want ErrUnknownMembership", err)
	}
}

// TestChangeMemberRoleRejectsUnknownRole keeps the input gate
// honest: only built-in role strings (admin/member/viewer) pass.
func TestChangeMemberRoleRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	seedMembership(t, store, DefaultOrgName, "fp-bob", "member")
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	_, err := store.ChangeMemberRole(ctx, org.ID, "fp-bob", "superuser")
	if err == nil || !contains(err.Error(), "built-in role") {
		t.Fatalf("err: got %v want built-in-role complaint", err)
	}
}

// TestRemoveMemberDeletesAndReturnsPrior covers the happy path
// for the remove flow.
func TestRemoveMemberDeletesAndReturnsPrior(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	seedMembership(t, store, DefaultOrgName, "fp-bob", "admin")
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	prior, err := store.RemoveMember(ctx, org.ID, "fp-bob")
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if prior != "admin" {
		t.Errorf("prior: got %q want admin", prior)
	}
	now, _ := store.RoleFor(context.Background(), org.ID, "fp-bob")
	if now != "" {
		t.Errorf("membership still present: %q", now)
	}
}

// TestRemoveMemberUnknownMembershipIsErrUnknownMembership
// mirrors the ChangeMemberRole sentinel branch.
func TestRemoveMemberUnknownMembershipIsErrUnknownMembership(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	_, err := store.RemoveMember(ctx, org.ID, "fp-ghost")
	if !errors.Is(err, ErrUnknownMembership) {
		t.Fatalf("err: got %v want ErrUnknownMembership", err)
	}
}
