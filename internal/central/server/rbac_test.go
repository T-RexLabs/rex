package server

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/rex/internal/core/rbac"
)

// TestRoleForReturnsMembershipRole exercises the PostgresStore
// implementation of RoleResolver against a fresh schema. The default
// org seed has no memberships, so we insert one then read it back.
func TestRoleForReturnsMembershipRole(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	if err := s.EnsureDefaultMembership(context.Background(), "fp-alice"); err != nil {
		t.Fatalf("EnsureDefaultMembership: %v", err)
	}
	org, err := s.LookupOrg(context.Background(), DefaultOrgName)
	if err != nil {
		t.Fatalf("LookupOrg: %v", err)
	}
	role, err := s.RoleFor(context.Background(), org.ID, "fp-alice")
	if err != nil {
		t.Fatalf("RoleFor: %v", err)
	}
	if role != string(rbac.RoleMember) {
		t.Fatalf("role: got %q want %q", role, rbac.RoleMember)
	}
}

func TestRoleForReturnsEmptyWhenNoMembership(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	org, err := s.LookupOrg(context.Background(), DefaultOrgName)
	if err != nil {
		t.Fatalf("LookupOrg: %v", err)
	}
	role, err := s.RoleFor(context.Background(), org.ID, "fp-stranger")
	if err != nil {
		t.Fatalf("RoleFor: %v", err)
	}
	if role != "" {
		t.Fatalf("expected empty role for non-member; got %q", role)
	}
}

// TestRequirePermissionMembershipMember covers the full happy path:
// PostgresStore-backed Server, fingerprint with a 'member' role,
// requesting an action that role holds. Should pass without error.
func TestRequirePermissionMembershipMember(t *testing.T) {
	t.Parallel()

	store, _ := freshPostgresStore(t)
	if err := store.EnsureDefaultMembership(context.Background(), "fp-alice"); err != nil {
		t.Fatalf("EnsureDefaultMembership: %v", err)
	}
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	srv, err := New(Options{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	if err := srv.requirePermission(context.Background(), "fp-alice", org.ID, rbac.PermSyncPush, "", "", ""); err != nil {
		t.Fatalf("member should hold sync.push: %v", err)
	}
	// Members do NOT hold rbac.grant.
	err = srv.requirePermission(context.Background(), "fp-alice", org.ID, rbac.PermRBACGrant, "", "", "")
	if err == nil {
		t.Fatal("member should not hold rbac.grant")
	}
}

// TestRequirePermissionDeniesNonMember covers a fingerprint that
// authenticated and was tenant-routed but has NO membership row in
// the resolved org — denied with a structured error.
func TestRequirePermissionDeniesNonMember(t *testing.T) {
	t.Parallel()

	store, _ := freshPostgresStore(t)
	org, _ := store.LookupOrg(context.Background(), DefaultOrgName)

	srv, err := New(Options{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	err = srv.requirePermission(context.Background(), "fp-stranger", org.ID, rbac.PermSyncPull, "", "", "")
	if err == nil {
		t.Fatal("non-member should be denied")
	}
	var denied *rbacDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("error type: %T %v", err, err)
	}
	if denied.action != rbac.PermSyncPull {
		t.Errorf("action: got %q", denied.action)
	}
}

// TestRequirePermissionMemoryStorePassthrough covers the dev-mode
// shortcut: when the Store has no RoleResolver, requirePermission
// returns nil regardless of the action. Keeps `rex serve --db ""`
// usable without RBAC infrastructure.
func TestRequirePermissionMemoryStorePassthrough(t *testing.T) {
	t.Parallel()

	srv, err := New(Options{}) // default MemoryStore, no resolver
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.requirePermission(context.Background(), "fp", "org", rbac.PermRBACGrant, "", "", ""); err != nil {
		t.Fatalf("MemoryStore path should pass: %v", err)
	}
}
