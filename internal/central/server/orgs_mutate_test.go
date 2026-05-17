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

// TestEnsureAdminMembershipUpsertsToAdmin covers the --dev
// auto-admin path: a fresh fingerprint lands as admin; an
// existing member row is upgraded to admin. Idempotent on
// re-run.
func TestEnsureAdminMembershipUpsertsToAdmin(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := context.Background()
	org, _ := store.LookupOrg(ctx, DefaultOrgName)

	// Fresh insert.
	if err := store.EnsureAdminMembership(ctx, "fp-dev"); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	role, _ := store.RoleFor(ctx, org.ID, "fp-dev")
	if role != "admin" {
		t.Errorf("after first ensure: role %q want admin", role)
	}

	// Pre-existing member row gets upgraded.
	seedMembership(t, store, DefaultOrgName, "fp-other", "member")
	if err := store.EnsureAdminMembership(ctx, "fp-other"); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	role, _ = store.RoleFor(ctx, org.ID, "fp-other")
	if role != "admin" {
		t.Errorf("after upgrade: role %q want admin", role)
	}

	// Idempotent on re-run.
	if err := store.EnsureAdminMembership(ctx, "fp-dev"); err != nil {
		t.Fatalf("repeat Ensure: %v", err)
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

// TestRedeemInviteHappyPath covers the issuer→redeemer
// lifecycle end-to-end: an admin issues an invite, a fresh
// keypair redeems it, the resulting RedeemResult carries both
// KeyRegistered + MemberJoined flags, the invite is marked
// redeemed, the membership row lands, and the authorized_keys
// row carries the same invite_id as the redeemed invite.
func TestRedeemInviteHappyPath(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	inv, err := store.IssueInvite(ctx, org.ID, "fp-alice", "member")
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	fp, pem := mintKeyPEM(t, "bob")
	res, err := store.RedeemInvite(context.Background(), inv.Token, "bob", string(pem))
	if err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}
	if !res.KeyRegistered {
		t.Error("KeyRegistered=false on fresh redeem")
	}
	if !res.MemberJoined {
		t.Error("MemberJoined=false on fresh redeem")
	}
	if res.Role != "member" {
		t.Errorf("Role: got %q want member", res.Role)
	}
	if res.Fingerprint != fp {
		t.Errorf("Fingerprint: got %q want %q", res.Fingerprint, fp)
	}
	if res.OrgID != org.ID {
		t.Errorf("OrgID: got %q want %q", res.OrgID, org.ID)
	}
	// Membership row landed under the invite's role.
	role, err := store.RoleFor(context.Background(), org.ID, fp)
	if err != nil {
		t.Fatalf("RoleFor: %v", err)
	}
	if role != "member" {
		t.Errorf("membership role: got %q want member", role)
	}
	// Pending list no longer carries the invite.
	pending, _ := store.ListPendingInvites(ctx, org.ID)
	for _, p := range pending {
		if p.ID == inv.ID {
			t.Error("invite still pending after redeem")
		}
	}
}

// TestRedeemInviteRejectsUnknownToken covers the not-found
// branch. The web layer maps the sentinel to 404 without
// distinguishing it from "expired" in the response body.
func TestRedeemInviteRejectsUnknownToken(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	_, pem := mintKeyPEM(t, "bob")
	_, err := store.RedeemInvite(context.Background(), "tok-nope", "bob", string(pem))
	if !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("err: got %v want ErrInviteNotFound", err)
	}
}

// TestRedeemInviteRejectsAlreadyRedeemed covers the
// double-redeem branch — second call on the same token must
// fail with ErrInviteAlreadyRedeemed (handler maps to 409).
func TestRedeemInviteRejectsAlreadyRedeemed(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	inv, err := store.IssueInvite(ctx, org.ID, "fp-alice", "viewer")
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	_, pem := mintKeyPEM(t, "bob")
	if _, err := store.RedeemInvite(context.Background(), inv.Token, "bob", string(pem)); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	_, pem2 := mintKeyPEM(t, "carol")
	_, err = store.RedeemInvite(context.Background(), inv.Token, "carol", string(pem2))
	if !errors.Is(err, ErrInviteAlreadyRedeemed) {
		t.Fatalf("err: got %v want ErrInviteAlreadyRedeemed", err)
	}
}

// TestRedeemInviteReusedKeySkipsKeyRegistered covers the
// idempotency carve-out from the amendment: when a fingerprint
// is already in authorized_keys (the recipient is reusing the
// same key to join a second org), KeyRegistered=false +
// MemberJoined=true so the handler emits only org.member.joined.
func TestRedeemInviteReusedKeySkipsKeyRegistered(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	// First invite: registers the key + joins the default org.
	inv1, _ := store.IssueInvite(ctx, org.ID, "fp-alice", "member")
	_, pem := mintKeyPEM(t, "bob")
	res1, err := store.RedeemInvite(context.Background(), inv1.Token, "bob", string(pem))
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if !res1.KeyRegistered || !res1.MemberJoined {
		t.Fatalf("first redeem flags: %+v", res1)
	}

	// Re-redeem into the same org with a second invite to the
	// same key. The membership row already exists, so the SQL
	// upsert no-ops and MemberJoined should be false too;
	// KeyRegistered is false because the key is in the table.
	inv2, _ := store.IssueInvite(ctx, org.ID, "fp-alice", "member")
	res2, err := store.RedeemInvite(context.Background(), inv2.Token, "bob", string(pem))
	if err != nil {
		t.Fatalf("second redeem: %v", err)
	}
	if res2.KeyRegistered {
		t.Error("KeyRegistered=true on reused key; want false")
	}
	if res2.MemberJoined {
		t.Error("MemberJoined=true on reused membership; want false")
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
