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

// TestIssueInviteCreatesRowAndReturnsToken covers the issuer
// side: a fresh row lands in org_invites with a non-empty token,
// the role + invited_by ride through, and expires_at lands in
// the future (the 7-day TTL is implicit in the SQL).
func TestIssueInviteCreatesRowAndReturnsToken(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	inv, err := store.IssueInvite(ctx, org.ID, "fp-alice", "viewer")
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	if inv.Token == "" {
		t.Error("Token is empty")
	}
	if inv.Role != "viewer" {
		t.Errorf("Role: got %q want viewer", inv.Role)
	}
	if inv.InvitedBy != "fp-alice" {
		t.Errorf("InvitedBy: got %q want fp-alice", inv.InvitedBy)
	}
	if !inv.ExpiresAt.After(inv.CreatedAt) {
		t.Errorf("ExpiresAt %s not after CreatedAt %s", inv.ExpiresAt, inv.CreatedAt)
	}
}

// TestIssueInviteRejectsUnknownRole keeps the input gate honest
// at the server layer (mirrors ChangeMemberRole's check).
func TestIssueInviteRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	_, err := store.IssueInvite(ctx, org.ID, "fp-alice", "founder")
	if err == nil || !contains(err.Error(), "built-in role") {
		t.Fatalf("err: got %v want built-in-role complaint", err)
	}
}

// TestListPendingInvitesReturnsIssuedRows covers the read side:
// after issuing two invites both appear in the pending list.
func TestListPendingInvitesReturnsIssuedRows(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	for _, role := range []string{"viewer", "member"} {
		if _, err := store.IssueInvite(ctx, org.ID, "fp-alice", role); err != nil {
			t.Fatalf("IssueInvite %s: %v", role, err)
		}
	}
	invs, err := store.ListPendingInvites(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListPendingInvites: %v", err)
	}
	if len(invs) != 2 {
		t.Fatalf("invites: got %d want 2", len(invs))
	}
	for _, inv := range invs {
		if inv.Token == "" || inv.Role == "" {
			t.Errorf("malformed invite: %+v", inv)
		}
	}
}
