package rbac

import (
	"strings"
	"testing"
	"time"
)

func TestPermissionsCatalogStable(t *testing.T) {
	t.Parallel()

	got := Permissions()
	if len(got) == 0 {
		t.Fatal("Permissions returned empty")
	}
	// Sorted, no duplicates.
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("permissions not sorted/unique at %d: %s vs %s", i, got[i-1], got[i])
		}
	}
}

func TestBuiltinRolesPresent(t *testing.T) {
	t.Parallel()

	for _, r := range []Role{RoleAdmin, RoleMember, RoleViewer} {
		if !IsBuiltinRole(r) {
			t.Errorf("%q should be built-in", r)
		}
		if _, ok := RolePermissions(r); !ok {
			t.Errorf("RolePermissions(%q) returned !ok", r)
		}
	}
	if IsBuiltinRole("nonsense") {
		t.Error("nonsense should not be built-in")
	}
}

// TestAdminHoldsEverything: admin is the closure over Permissions().
func TestAdminHoldsEverything(t *testing.T) {
	t.Parallel()

	perms, _ := RolePermissions(RoleAdmin)
	for _, p := range Permissions() {
		if _, ok := perms[p]; !ok {
			t.Errorf("admin missing %q", p)
		}
	}
}

// TestViewerIsReadOnly confirms viewer has no permissions whose
// action half is anything but a read-class verb.
func TestViewerIsReadOnly(t *testing.T) {
	t.Parallel()

	allowedActions := map[string]bool{
		"read": true, "pull": true, "query": true,
	}
	perms, _ := RolePermissions(RoleViewer)
	for p := range perms {
		parts := strings.SplitN(string(p), ".", 2)
		if len(parts) != 2 {
			t.Errorf("viewer permission %q is malformed", p)
			continue
		}
		if !allowedActions[parts[1]] {
			t.Errorf("viewer should not hold write-class %q", p)
		}
	}
}

func TestAllowDeniesUnknownIdentity(t *testing.T) {
	t.Parallel()

	d := Allow(Request{
		Fingerprint: "fp-alice", OrgID: "org-1", Action: PermSpecRead,
	}, nil)
	if d.Allowed {
		t.Fatal("expected deny with no grants")
	}
	if d.Reason == "" {
		t.Error("decision should carry a reason")
	}
}

func TestAllowMatchesByRole(t *testing.T) {
	t.Parallel()

	g := Grant{Fingerprint: "fp-alice", OrgID: "org-1", Role: RoleMember}
	d := Allow(Request{
		Fingerprint: "fp-alice", OrgID: "org-1", Action: PermRunInvoke,
	}, []Grant{g})
	if !d.Allowed {
		t.Fatalf("member should hold run.invoke: %s", d.Reason)
	}
}

func TestAllowDeniesActionOutsideRole(t *testing.T) {
	t.Parallel()

	g := Grant{Fingerprint: "fp-bob", OrgID: "org-1", Role: RoleViewer}
	d := Allow(Request{
		Fingerprint: "fp-bob", OrgID: "org-1", Action: PermSpecEdit,
	}, []Grant{g})
	if d.Allowed {
		t.Fatal("viewer should not have spec.edit")
	}
	if !strings.Contains(d.Reason, "viewer") {
		t.Errorf("reason should mention the role: %s", d.Reason)
	}
}

func TestAllowSkipsCrossOrgGrants(t *testing.T) {
	t.Parallel()

	g := Grant{Fingerprint: "fp-alice", OrgID: "org-OTHER", Role: RoleAdmin}
	d := Allow(Request{
		Fingerprint: "fp-alice", OrgID: "org-1", Action: PermSpecRead,
	}, []Grant{g})
	if d.Allowed {
		t.Fatal("grant in another org must not apply")
	}
}

func TestAllowSkipsCrossIdentityGrants(t *testing.T) {
	t.Parallel()

	g := Grant{Fingerprint: "fp-bob", OrgID: "org-1", Role: RoleAdmin}
	d := Allow(Request{
		Fingerprint: "fp-alice", OrgID: "org-1", Action: PermSpecRead,
	}, []Grant{g})
	if d.Allowed {
		t.Fatal("grant for a different fingerprint must not apply")
	}
}

func TestAllowEnforcesWorkspaceScope(t *testing.T) {
	t.Parallel()

	g := Grant{
		Fingerprint: "fp", OrgID: "o", Role: RoleMember,
		Constraint: Constraint{Workspaces: []string{"ws-a"}},
	}
	yes := Allow(Request{
		Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, Workspace: "ws-a",
	}, []Grant{g})
	no := Allow(Request{
		Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, Workspace: "ws-b",
	}, []Grant{g})
	if !yes.Allowed {
		t.Fatalf("ws-a request should pass: %s", yes.Reason)
	}
	if no.Allowed {
		t.Fatal("ws-b request should fail")
	}
}

func TestAllowEnforcesHarnessScope(t *testing.T) {
	t.Parallel()

	g := Grant{
		Fingerprint: "fp", OrgID: "o", Role: RoleMember,
		Constraint: Constraint{Harnesses: []string{"claude-code"}},
	}
	yes := Allow(Request{
		Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, Harness: "claude-code",
	}, []Grant{g})
	no := Allow(Request{
		Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, Harness: "codex",
	}, []Grant{g})
	if !yes.Allowed {
		t.Fatalf("claude-code request should pass: %s", yes.Reason)
	}
	if no.Allowed {
		t.Fatal("codex request should fail per RBAC.4.2")
	}
}

func TestAllowEnforcesTimeWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	g := Grant{
		Fingerprint: "fp", OrgID: "o", Role: RoleMember,
		Constraint: Constraint{TimeWindow: TimeWindow{
			Start: now.Add(-time.Hour),
			End:   now.Add(time.Hour),
		}},
	}
	if d := Allow(Request{
		Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, At: now,
	}, []Grant{g}); !d.Allowed {
		t.Fatalf("request inside window should pass: %s", d.Reason)
	}
	if d := Allow(Request{
		Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke,
		At: now.Add(2 * time.Hour),
	}, []Grant{g}); d.Allowed {
		t.Fatal("request outside window should fail")
	}
}

func TestAllowAggregatesMultipleGrants(t *testing.T) {
	t.Parallel()

	// One grant scoped to ws-a, another to ws-b. The user has
	// access to both via different grants.
	grants := []Grant{
		{Fingerprint: "fp", OrgID: "o", Role: RoleMember, Constraint: Constraint{Workspaces: []string{"ws-a"}}},
		{Fingerprint: "fp", OrgID: "o", Role: RoleViewer, Constraint: Constraint{Workspaces: []string{"ws-b"}}},
	}
	wsa := Allow(Request{Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, Workspace: "ws-a"}, grants)
	wsbInvoke := Allow(Request{Fingerprint: "fp", OrgID: "o", Action: PermRunInvoke, Workspace: "ws-b"}, grants)
	wsbRead := Allow(Request{Fingerprint: "fp", OrgID: "o", Action: PermSpecRead, Workspace: "ws-b"}, grants)
	if !wsa.Allowed {
		t.Errorf("member grant covers ws-a invoke: %s", wsa.Reason)
	}
	if wsbInvoke.Allowed {
		t.Error("viewer grant should not allow run.invoke on ws-b")
	}
	if !wsbRead.Allowed {
		t.Errorf("viewer grant covers ws-b spec.read: %s", wsbRead.Reason)
	}
}

func TestIsKnownPermission(t *testing.T) {
	t.Parallel()

	if !IsKnownPermission(PermSpecRead) {
		t.Error("spec.read should be known")
	}
	if IsKnownPermission("not.a.perm") {
		t.Error("typo should be unknown")
	}
}

func TestUnknownRoleProducesReason(t *testing.T) {
	t.Parallel()

	g := Grant{Fingerprint: "fp", OrgID: "o", Role: Role("custodian")}
	d := Allow(Request{Fingerprint: "fp", OrgID: "o", Action: PermSpecRead}, []Grant{g})
	if d.Allowed {
		t.Fatal("unknown role must not allow")
	}
	if !strings.Contains(d.Reason, "custodian") {
		t.Errorf("reason should name the unknown role: %s", d.Reason)
	}
}
