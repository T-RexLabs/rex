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

// InviteRow is one pending invite on the /orgs/<id>/members
// page. The token rides through so the issuer can copy it for
// out-of-band delivery; the admin who issued the invite is the
// only viewer the page renders it to (the token persists in
// the db so a future admin can re-fetch via list).
type InviteRow struct {
	ID        string
	Token     string
	Role      string
	InvitedBy string
	ExpiresAt time.Time
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
// handlers query, plus the small mutation surface admins drive
// from the central web UI. v1 implementations wrap the central
// server's PostgresStore methods directly.
//
// Mutation operations (ChangeMemberRole, RemoveMember) are
// admin-only on the web side — the handlers gate via RoleFor
// returning "admin" before invoking — and emit audit events on
// success. Adding more operations (invite issuance, etc.) is
// additive to this interface.
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
	// IssueInvite mints a new invite for orgID with the given
	// role and returns the populated InviteRow so the admin
	// can copy the token for out-of-band delivery. inviter is
	// the authenticated caller's fingerprint, stamped into
	// both the row and the audit event.
	IssueInvite(orgID, inviter, role string) (InviteRow, error)
	// ListPendingInvites returns the org's unredeemed
	// unexpired invites for display on the members page.
	ListPendingInvites(orgID string) ([]InviteRow, error)
	// ChangeMemberRole updates an existing member's role and
	// returns the prior role so callers can audit the
	// transition. changerFingerprint is the authenticated
	// caller's identity (from the session gate) — the adapter
	// stamps it into the org.member.role_changed audit event so
	// reviewers can trace who did what. Returns
	// ErrUnknownMembership when no row matches; admin-only on
	// the central web side.
	ChangeMemberRole(orgID, fingerprint, newRole, changerFingerprint string) (priorRole string, err error)
	// RemoveMember deletes a membership and returns the prior
	// role for the audit trail. removerFingerprint plays the
	// same role as changerFingerprint on ChangeMemberRole.
	// Returns ErrUnknownMembership when no row matches.
	RemoveMember(orgID, fingerprint, removerFingerprint string) (priorRole string, err error)
}

// ErrUnknownMembership is the sentinel ChangeMemberRole +
// RemoveMember return when no row matches (orgID, fingerprint).
// The web handler maps it to 404.
var ErrUnknownMembership = errOrgsUnknownMembership

// errOrgsUnknownMembership backs ErrUnknownMembership. Wrapped
// in a private name so callers compare via errors.Is rather than
// taking the address of an exported variable.
var errOrgsUnknownMembership = orgsError("unknown membership")

type orgsError string

func (e orgsError) Error() string { return string(e) }
