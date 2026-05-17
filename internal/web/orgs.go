package web

import "time"

// OrgSummary is the lightweight shape the central org-admin
// surfaces render. Mirrors the central server's Org type without
// importing it so internal/web stays a leaf package.
type OrgSummary struct {
	ID          string
	Name        string
	DisplayName string
	CreatedAt   time.Time
}

// MembershipRow is one row on the /orgs/<id>/members page.
// Joined timestamps render via the template's `time` formatting.
type MembershipRow struct {
	Fingerprint string
	Role        string
	JoinedAt    time.Time
}

// RoleCatalogRow is one row on the /orgs/<id>/roles page. Lists
// the rbac role + its assigned permissions. The catalog is
// static today (built from internal/core/rbac); the page surfaces
// it so admins know what role grants what.
type RoleCatalogRow struct {
	Role        string
	Permissions []string
}

// OrgsProjection is the read-side surface the central org-admin
// handlers query. v1 implementations wrap the central server's
// PostgresStore methods (LookupOrgByID, ListMembersForOrg,
// ListOrgs, RoleFor); mutation surfaces (invite, role change,
// removal) are tracked under central-node.RBAC-SVR.1 and are
// deliberately out of this interface.
type OrgsProjection interface {
	// LookupOrg returns the org for the page header. found is
	// false when the id is unknown; the handler 404s.
	LookupOrg(orgID string) (OrgSummary, bool, error)
	// ListMembers returns membership rows for orgID, sorted by
	// fingerprint.
	ListMembers(orgID string) ([]MembershipRow, error)
	// RoleFor returns the role the fingerprint holds in orgID,
	// or "" when no membership exists. Used by every
	// org-scoped web handler to verify the authenticated
	// identity actually belongs to the org in the URL before
	// rendering any data (CENTRAL.3 + identity-and-trust.RBAC.1).
	// Returns a non-nil error only on storage failures the
	// handler should surface as 500.
	RoleFor(orgID, fingerprint string) (string, error)
}
