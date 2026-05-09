// Package rbac is the role-based access control engine
// (identity-and-trust.RBAC). The package answers questions of the
// form "may identity I perform action A on resource R in org O?" —
// see RBAC.1.
//
// Permission catalog (RBAC.3): permissions are strings of the form
// `<resource-type>.<action>`. The set is closed and grows additively;
// readers must skip unknown actions rather than erroring (overview.SYS.3
// generalised). The catalog enumerates every action the central node
// gates and is the single source of truth — adding an action means
// adding it to Permissions(), the role tables, and (separately) the
// handler that calls Allow.
//
// Default roles (RBAC.2):
//
//   - admin   — every action in the catalog
//   - member  — workspace.*, spec.*, run.*, search.*, sync.push/pull,
//     git.push/pull
//   - viewer  — *.read-class actions plus search/audit reads
//
// Fine-grained scoping (RBAC.4) is structurally supported via Grant
// constraints (Workspaces, Harnesses, Tools, TimeWindow). The default
// roles ship with no scoping constraints — a member of an org has
// member permissions across every workspace in that org. Custom
// per-grant scoping lands when an admin surface allows it; v1 admin
// surfaces are CLI-only and `rex rbac grant` is deferred.
package rbac

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Permission is a `<resource-type>.<action>` string. The string form
// is the wire format (audit logs, role definitions on disk); avoid
// constructing arbitrary strings outside the catalog.
type Permission string

// Role names a built-in or custom role. Built-in role values are
// Postgres-friendly lowercase strings that match the org_memberships
// `role` column.
type Role string

// Built-in role identifiers (RBAC.2).
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// Catalog of permissions. Keep this list sorted alphabetically and
// extend it additively.
const (
	// Workspace lifecycle.
	PermWorkspaceCreate  Permission = "workspace.create"
	PermWorkspaceRead    Permission = "workspace.read"
	PermWorkspaceUpdate  Permission = "workspace.update"
	PermWorkspaceArchive Permission = "workspace.archive"
	PermWorkspaceDelete  Permission = "workspace.delete"

	// Specs (git-merged content).
	PermSpecRead   Permission = "spec.read"
	PermSpecCreate Permission = "spec.create"
	PermSpecEdit   Permission = "spec.edit"
	PermSpecDelete Permission = "spec.delete"

	// Runs (harness invocations).
	PermRunRead    Permission = "run.read"
	PermRunInvoke  Permission = "run.invoke"
	PermRunCancel  Permission = "run.cancel"
	PermRunApprove Permission = "run.approve" // permission prompts

	// Sync (event-sourced spine).
	PermSyncPush Permission = "sync.push"
	PermSyncPull Permission = "sync.pull"

	// Sync (git-merged content).
	PermGitPush Permission = "git.push"
	PermGitPull Permission = "git.pull"

	// Search + audit (read-only by design).
	PermSearchQuery Permission = "search.query"
	PermAuditRead   Permission = "audit.read"

	// RBAC self-administration. Only admins should hold these.
	PermRBACGrant Permission = "rbac.grant"
	PermRBACRead  Permission = "rbac.read"
)

// Permissions returns the canonical catalog in lex order. Used by
// `rex rbac` admin surfaces and by tests to assert role tables stay
// in sync with the catalog.
func Permissions() []Permission {
	out := []Permission{
		PermAuditRead,
		PermGitPull,
		PermGitPush,
		PermRBACGrant,
		PermRBACRead,
		PermRunApprove,
		PermRunCancel,
		PermRunInvoke,
		PermRunRead,
		PermSearchQuery,
		PermSpecCreate,
		PermSpecDelete,
		PermSpecEdit,
		PermSpecRead,
		PermSyncPull,
		PermSyncPush,
		PermWorkspaceArchive,
		PermWorkspaceCreate,
		PermWorkspaceDelete,
		PermWorkspaceRead,
		PermWorkspaceUpdate,
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// builtinRolePermissions is the fixed table of role → permission sets.
// admin always holds every catalog entry; viewer is the read-only
// closure; member is what a typical IC needs day-to-day. The map is
// constructed once at package init from the three slices below to keep
// the additive-only intent visible.
var builtinRolePermissions = func() map[Role]map[Permission]struct{} {
	admin := map[Permission]struct{}{}
	for _, p := range Permissions() {
		admin[p] = struct{}{}
	}

	member := setOf(
		PermWorkspaceRead,
		PermSpecRead, PermSpecCreate, PermSpecEdit,
		PermRunRead, PermRunInvoke, PermRunCancel, PermRunApprove,
		PermSyncPush, PermSyncPull,
		PermGitPush, PermGitPull,
		PermSearchQuery,
		PermAuditRead,
	)

	viewer := setOf(
		PermWorkspaceRead,
		PermSpecRead,
		PermRunRead,
		PermSyncPull,
		PermGitPull,
		PermSearchQuery,
		PermAuditRead,
	)

	return map[Role]map[Permission]struct{}{
		RoleAdmin:  admin,
		RoleMember: member,
		RoleViewer: viewer,
	}
}()

func setOf(ps ...Permission) map[Permission]struct{} {
	out := make(map[Permission]struct{}, len(ps))
	for _, p := range ps {
		out[p] = struct{}{}
	}
	return out
}

// IsBuiltinRole reports whether r is one of the three default roles.
// Custom roles can land later additively without disturbing this
// predicate.
func IsBuiltinRole(r Role) bool {
	_, ok := builtinRolePermissions[r]
	return ok
}

// RolePermissions returns the permission set bound to a role, or
// (nil, false) for an unknown role. The returned map is the package
// internal map by reference; callers must NOT mutate it.
func RolePermissions(r Role) (map[Permission]struct{}, bool) {
	got, ok := builtinRolePermissions[r]
	return got, ok
}

// Constraint optionally narrows a Grant. A zero-value Constraint is
// the unrestricted default (matches every (workspace, harness, tool)
// at any time). Empty slices and a zero TimeWindow each disable that
// dimension.
type Constraint struct {
	// Workspaces, when non-empty, restricts the grant to the listed
	// workspace ids.
	Workspaces []string
	// Harnesses, when non-empty, restricts the grant to the listed
	// harness names. Drives RBAC.4.2's "claude-code but not codex"
	// scoping.
	Harnesses []string
	// Tools, when non-empty, restricts the grant to the listed MCP
	// server / app integration names.
	Tools []string
	// TimeWindow, when non-zero, restricts the grant to the
	// inclusive [Start, End] range. Evaluated against the central
	// node's clock per RBAC.4.1.
	TimeWindow TimeWindow
}

// TimeWindow is a closed [Start, End] interval. Zero values for
// Start or End mean "unbounded on this end".
type TimeWindow struct {
	Start time.Time
	End   time.Time
}

// IsZero reports whether tw is the unrestricted default.
func (tw TimeWindow) IsZero() bool {
	return tw.Start.IsZero() && tw.End.IsZero()
}

// Contains reports whether t falls inside tw. A zero-value tw matches
// every time.
func (tw TimeWindow) Contains(t time.Time) bool {
	if !tw.Start.IsZero() && t.Before(tw.Start) {
		return false
	}
	if !tw.End.IsZero() && t.After(tw.End) {
		return false
	}
	return true
}

// Grant binds an identity to a role within an org, optionally with
// constraints that narrow the role's effective permissions.
type Grant struct {
	Fingerprint string // identity's public-key fingerprint
	OrgID       string
	Role        Role
	Constraint  Constraint
}

// Request is the input to the engine: who wants to do what, where,
// and when.
type Request struct {
	Fingerprint string
	OrgID       string
	Action      Permission
	// Workspace is the workspace id the action targets, when one
	// is involved (run.invoke, sync.push, …). Empty for org-wide
	// actions.
	Workspace string
	// Harness is the harness name a run-class action targets.
	Harness string
	// Tool is the MCP server / app integration name a tool action
	// targets.
	Tool string
	// At is the time the request is being evaluated. Zero falls
	// through to time.Now (so unit tests can pin it without every
	// caller threading time).
	At time.Time
}

// Decision is the engine's typed response. Allowed is the bottom
// line; Reason is a human-readable string for audit logging on a
// deny.
type Decision struct {
	Allowed bool
	Reason  string
}

// Allow is the engine's only entry point: given a list of grants
// applicable to (req.Fingerprint, req.OrgID), decide. The decision
// is yes if any single grant covers the request; otherwise no with
// a human-readable reason.
//
// Grants are passed in rather than fetched from a store so the
// engine stays a pure function — the central node's tenant
// middleware loads grants from the database and hands them to Allow.
func Allow(req Request, grants []Grant) Decision {
	if req.Action == "" {
		return Decision{Reason: "missing action"}
	}
	if req.Fingerprint == "" {
		return Decision{Reason: "missing identity"}
	}
	if req.OrgID == "" {
		return Decision{Reason: "missing org"}
	}
	at := req.At
	if at.IsZero() {
		at = time.Now()
	}

	denyReasons := []string{}
	for _, g := range grants {
		if g.Fingerprint != req.Fingerprint {
			continue
		}
		if g.OrgID != req.OrgID {
			continue
		}
		perms, ok := RolePermissions(g.Role)
		if !ok {
			denyReasons = append(denyReasons, fmt.Sprintf("unknown role %q", g.Role))
			continue
		}
		if _, has := perms[req.Action]; !has {
			denyReasons = append(denyReasons, fmt.Sprintf("role %q does not include %q", g.Role, req.Action))
			continue
		}
		if reason, ok := matchConstraint(g.Constraint, req, at); !ok {
			denyReasons = append(denyReasons, reason)
			continue
		}
		return Decision{Allowed: true}
	}
	if len(denyReasons) == 0 {
		return Decision{Reason: "no grant matched (identity, org)"}
	}
	return Decision{Reason: strings.Join(denyReasons, "; ")}
}

// matchConstraint returns (reason, ok). ok=true means the constraint
// permits the request; ok=false carries a human-readable reason.
func matchConstraint(c Constraint, req Request, at time.Time) (string, bool) {
	if len(c.Workspaces) > 0 && req.Workspace != "" {
		if !contains(c.Workspaces, req.Workspace) {
			return fmt.Sprintf("workspace %q outside grant scope", req.Workspace), false
		}
	}
	if len(c.Harnesses) > 0 && req.Harness != "" {
		if !contains(c.Harnesses, req.Harness) {
			return fmt.Sprintf("harness %q outside grant scope", req.Harness), false
		}
	}
	if len(c.Tools) > 0 && req.Tool != "" {
		if !contains(c.Tools, req.Tool) {
			return fmt.Sprintf("tool %q outside grant scope", req.Tool), false
		}
	}
	if !c.TimeWindow.IsZero() && !c.TimeWindow.Contains(at) {
		return "outside grant time window", false
	}
	return "", true
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// IsKnownPermission reports whether p appears in the catalog. Used
// by the central server to refuse Allow calls for typo'd actions
// rather than silently falling through to "deny" with a confusing
// reason.
func IsKnownPermission(p Permission) bool {
	for _, q := range Permissions() {
		if q == p {
			return true
		}
	}
	return false
}

// ErrUnknownPermission is the sentinel for the catalog check above.
// Wrapped errors that callers want to branch on use this.
var ErrUnknownPermission = errors.New("rbac: unknown permission")
