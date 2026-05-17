package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Org is the read-side projection of one row in the orgs table.
type Org struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Membership pairs an org with the role an identity holds in it.
// Used by /orgs/me-style endpoints (lands with tenant-routing)
// and by the middleware to resolve the request's org context.
type Membership struct {
	OrgID       string    `json:"org_id"`
	OrgName     string    `json:"org_name"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
	Fingerprint string    `json:"fingerprint"`
}

// MembershipEnsurer is the optional interface a Store can satisfy
// so the auth-verify hook can auto-join newly-authenticated
// identities to the seeded default org (central-node.TENANT.
// 4-note). PostgresStore implements this; MemoryStore does not
// (the in-memory dev/test path has no orgs).
//
// The interface is small on purpose — only what the auth path
// needs. Richer org admin lives in dedicated handlers a future
// PR adds.
type MembershipEnsurer interface {
	// EnsureDefaultMembership inserts a membership row binding
	// fp to the default org with the default role ('member')
	// when no row already exists for (default-org, fp). Safe to
	// call on every auth-verify success; the underlying SQL is
	// INSERT ... ON CONFLICT DO NOTHING.
	EnsureDefaultMembership(ctx context.Context, fingerprint string) error
}

// EnsureDefaultMembership upserts a (default org, fp) row if
// missing. Returns nil when the row exists (or was just
// created); errors only on database trouble.
func (s *PostgresStore) EnsureDefaultMembership(ctx context.Context, fingerprint string) error {
	if fingerprint == "" {
		return errors.New("server: empty fingerprint")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO org_memberships (org_id, fingerprint, role)
		SELECT id, $1, 'member'
		FROM   orgs
		WHERE  name = $2
		ON CONFLICT (org_id, fingerprint) DO NOTHING
	`, fingerprint, DefaultOrgName)
	if err != nil {
		return fmt.Errorf("server: ensure default membership: %w", err)
	}
	return nil
}

// LookupOrg returns the org with the given name, or pgx.ErrNoRows
// when missing. Used by EnsureDefaultMembership tests and the
// future tenant-routing middleware. ID is exposed as a string
// (UUID's text form) to keep callers free of pgx-internal types.
func (s *PostgresStore) LookupOrg(ctx context.Context, name string) (Org, error) {
	var o Org
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, name, display_name, created_at FROM orgs WHERE name = $1`,
		name,
	).Scan(&o.ID, &o.Name, &o.DisplayName, &o.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return o, fmt.Errorf("server: org %q not found", name)
		}
		return o, fmt.Errorf("server: lookup org %q: %w", name, err)
	}
	return o, nil
}

// ListMemberships returns every (org, role) pair the fingerprint
// belongs to. Empty slice when the fingerprint is unknown to the
// org system. Used by the tenant-routing middleware to disambiguate
// multi-org identities (TENANT.1-note).
func (s *PostgresStore) ListMemberships(ctx context.Context, fingerprint string) ([]Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.org_id::text, o.name, m.role, m.joined_at, m.fingerprint
		FROM   org_memberships m
		JOIN   orgs            o ON o.id = m.org_id
		WHERE  m.fingerprint = $1
		ORDER  BY o.name
	`, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("server: list memberships: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrgID, &m.OrgName, &m.Role, &m.JoinedAt, &m.Fingerprint); err != nil {
			return nil, fmt.Errorf("server: list memberships scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RoleFor returns the role string the fingerprint holds in orgID,
// or "" when no membership exists. Used by the RBAC gate to
// resolve (org, identity) → role on the request hot path; the
// interface is purposefully scalar so the lookup is one indexed
// row read.
func (s *PostgresStore) RoleFor(ctx context.Context, orgID, fingerprint string) (string, error) {
	if orgID == "" || fingerprint == "" {
		return "", errors.New("server: RoleFor requires orgID + fingerprint")
	}
	var role string
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM org_memberships WHERE org_id = $1 AND fingerprint = $2`,
		orgID, fingerprint,
	).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("server: role for (%s, %s): %w", orgID, fingerprint, err)
	}
	return role, nil
}

// RoleResolver is the optional interface a Store implements so the
// RBAC gate can resolve a role for a (orgID, fingerprint) pair on
// the request hot path. PostgresStore implements it; MemoryStore
// does not, which keeps the in-memory dev/test path RBAC-bypass
// (matches the keystore-empty bypass for signature verification).
type RoleResolver interface {
	RoleFor(ctx context.Context, orgID, fingerprint string) (string, error)
}

// ListOrgs returns every org the central knows about. The order
// is by name. Used by future admin surfaces (rex-central org
// list, the central web UI's /orgs page); nothing on the auth
// hot path calls it. Returns an empty slice on a fresh database
// — the seed insert is part of schema step 2, so calling
// ListOrgs after migration always yields at least one row
// ("default").
func (s *PostgresStore) ListOrgs(ctx context.Context) ([]Org, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, name, display_name, created_at FROM orgs ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("server: list orgs: %w", err)
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.DisplayName, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("server: list orgs scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// LookupOrgByID returns the org with the given id, or
// pgx.ErrNoRows when the id is unknown. The web shell's
// /orgs/<id> overview handler calls it; ListOrgs is the broader
// list-everything alternative.
func (s *PostgresStore) LookupOrgByID(ctx context.Context, id string) (Org, error) {
	var o Org
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, name, display_name, created_at FROM orgs WHERE id::text = $1`,
		id,
	).Scan(&o.ID, &o.Name, &o.DisplayName, &o.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return o, fmt.Errorf("server: org %q not found", id)
		}
		return o, fmt.Errorf("server: lookup org %q: %w", id, err)
	}
	return o, nil
}

// ChangeMemberRole updates an existing membership's role and
// returns the prior role so the caller can render or audit the
// transition. Returns ErrUnknownMembership when no row exists
// for (orgID, fingerprint); the handler surfaces that as 404
// rather than silently no-op'ing.
//
// Input validation: newRole must be one of the built-in roles
// (admin / member / viewer). Custom per-org roles are deferred
// — when they land the gate moves to a per-org role catalog.
func (s *PostgresStore) ChangeMemberRole(ctx context.Context, orgID, fingerprint, newRole string) (priorRole string, err error) {
	if orgID == "" || fingerprint == "" {
		return "", errors.New("server: ChangeMemberRole requires orgID + fingerprint")
	}
	if !isBuiltinRole(newRole) {
		return "", fmt.Errorf("server: role %q is not a built-in role (admin/member/viewer)", newRole)
	}
	err = s.withOrgScope(ctx, func(tx pgx.Tx) error {
		// Lock the row + read the prior role atomically.
		err := tx.QueryRow(ctx, `
			SELECT role FROM org_memberships
			WHERE  org_id = $1 AND fingerprint = $2
			FOR UPDATE
		`, orgID, fingerprint).Scan(&priorRole)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownMembership
			}
			return fmt.Errorf("server: lock membership: %w", err)
		}
		if priorRole == newRole {
			// No-op; caller can branch on priorRole == newRole
			// to skip audit emission.
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE org_memberships SET role = $3
			WHERE  org_id = $1 AND fingerprint = $2
		`, orgID, fingerprint, newRole); err != nil {
			return fmt.Errorf("server: update role: %w", err)
		}
		return nil
	})
	return priorRole, err
}

// RemoveMember deletes a membership row and returns the role
// the member held so the caller can audit what access was
// revoked. Returns ErrUnknownMembership when no row exists.
func (s *PostgresStore) RemoveMember(ctx context.Context, orgID, fingerprint string) (priorRole string, err error) {
	if orgID == "" || fingerprint == "" {
		return "", errors.New("server: RemoveMember requires orgID + fingerprint")
	}
	err = s.withOrgScope(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			DELETE FROM org_memberships
			WHERE  org_id = $1 AND fingerprint = $2
			RETURNING role
		`, orgID, fingerprint).Scan(&priorRole); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownMembership
			}
			return fmt.Errorf("server: delete membership: %w", err)
		}
		return nil
	})
	return priorRole, err
}

// ErrUnknownMembership is returned by ChangeMemberRole +
// RemoveMember when no row matches (orgID, fingerprint).
var ErrUnknownMembership = errors.New("server: unknown membership")

// isBuiltinRole reports whether s is one of the built-in role
// strings. Mirrors internal/core/rbac's catalog without an
// import (the central server already keeps these strings literal
// in the org_memberships schema's DEFAULT and the v1 carve-out
// for member-admin only supports the built-in roles).
func isBuiltinRole(s string) bool {
	switch s {
	case "admin", "member", "viewer":
		return true
	}
	return false
}

// ListMembersForOrg returns the membership rows for orgID,
// ordered by fingerprint so the list view is deterministic.
// Returns an empty slice + nil when the org has no members or
// when orgID does not exist (read-only surface — no separate
// "not found" branch).
func (s *PostgresStore) ListMembersForOrg(ctx context.Context, orgID string) ([]Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.org_id::text, o.name, m.role, m.joined_at, m.fingerprint
		FROM   org_memberships m
		JOIN   orgs            o ON o.id = m.org_id
		WHERE  m.org_id::text = $1
		ORDER  BY m.fingerprint
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("server: list members for org: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrgID, &m.OrgName, &m.Role, &m.JoinedAt, &m.Fingerprint); err != nil {
			return nil, fmt.Errorf("server: list members scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
