package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/asabla/rex/internal/core/identity"
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

// EnsureAdminMembership upserts a (default-org, fp, 'admin')
// row, used by the rex-central --dev flag to make the
// developer's local identity an admin without going through the
// bootstrap-token redeem dance. Idempotent: re-running on an
// already-admin row is a no-op; an existing member/viewer row
// is upgraded to admin.
//
// NEVER call this from a non-dev code path — it silently
// promotes whatever fingerprint is supplied without the
// bootstrap-token guard SEC.1 / BOOT.* depend on.
func (s *PostgresStore) EnsureAdminMembership(ctx context.Context, fingerprint string) error {
	if fingerprint == "" {
		return errors.New("server: EnsureAdminMembership requires fingerprint")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO org_memberships (org_id, fingerprint, role)
		SELECT id, $1, 'admin'
		FROM   orgs
		WHERE  name = $2
		ON CONFLICT (org_id, fingerprint) DO UPDATE SET role = 'admin'
	`, fingerprint, DefaultOrgName)
	if err != nil {
		return fmt.Errorf("server: ensure admin membership: %w", err)
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

// ListOrgsForFingerprint returns every org the fingerprint
// belongs to, sorted by name, projected as Org rows so the
// caller renders display_name + created_at without a second
// query. Backs the central web shell's GET / landing page
// (which renders a picker for multi-org users / redirects for
// single-org users).
func (s *PostgresStore) ListOrgsForFingerprint(ctx context.Context, fingerprint string) ([]Org, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id::text, o.name, o.display_name, o.created_at
		FROM   org_memberships m
		JOIN   orgs            o ON o.id = m.org_id
		WHERE  m.fingerprint = $1
		ORDER  BY o.name
	`, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("server: list orgs for fingerprint: %w", err)
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

// Invite is the read-side projection of one org_invites row.
// The token is included so the issuer can hand it to the
// invitee (the row is the source-of-truth for the token; v1
// shows it once on the issuer's screen and the admin can
// re-fetch via ListPendingInvites later).
type Invite struct {
	ID        string
	OrgID     string
	Token     string
	Role      string
	InvitedBy string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// IssueInvite creates a fresh invite row under (orgID, role)
// with a random token. The TTL is fixed at 7 days — generous
// enough for an admin to share via Slack/email, short enough
// that a leaked token can't be redeemed months later. Returns
// the populated Invite so the caller can render the token to
// the issuer for one-time copy.
//
// Input validation: role must be one of the built-in roles
// (admin / member / viewer).
func (s *PostgresStore) IssueInvite(ctx context.Context, orgID, invitedBy, role string) (Invite, error) {
	if orgID == "" {
		return Invite{}, errors.New("server: IssueInvite requires orgID")
	}
	if !isBuiltinRole(role) {
		return Invite{}, fmt.Errorf("server: role %q is not a built-in role (admin/member/viewer)", role)
	}
	var inv Invite
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO org_invites (org_id, invited_by, token, role, expires_at)
			VALUES ($1, $2,
			        replace(gen_random_uuid()::text, '-', '') ||
			        replace(gen_random_uuid()::text, '-', ''),
			        $3, now() + INTERVAL '7 days')
			RETURNING id::text, token, role, invited_by, expires_at, now() AS created_at
		`, orgID, invitedBy, role).Scan(
			&inv.ID, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("server: issue invite: %w", err)
		}
		inv.OrgID = orgID
		return nil
	})
	return inv, err
}

// RedeemResult captures the side effects of a successful
// RedeemInvite call. The caller (the redeem handler in
// cmd/rex-central) uses the *Inserted flags to gate which audit
// events to emit: a fresh fingerprint triggers
// identity.key_registered; a fresh membership row triggers
// org.member.joined. A re-redeem of the same key into a different
// org sees KeyRegistered=false + MemberJoined=true and emits only
// org.member.joined.
type RedeemResult struct {
	InviteID      string
	OrgID         string
	Fingerprint   string
	Handle        string
	Role          string
	KeyRegistered bool
	MemberJoined  bool
}

// Sentinel errors RedeemInvite returns. Handlers map these to
// distinct HTTP statuses (404 for unknown token, 410 for expired,
// 409 for already-redeemed) without leaking which condition the
// token tripped beyond the necessary user-facing nudge.
var (
	ErrInviteNotFound        = errors.New("server: invite not found")
	ErrInviteExpired         = errors.New("server: invite expired")
	ErrInviteAlreadyRedeemed = errors.New("server: invite already redeemed")
)

// RedeemInvite is the transactional unit behind the
// POST /invites/redeem flow (identity-and-trust.AUTH.2.1 + ORG.5).
// In a single Postgres transaction it:
//
//  1. Looks up the invite by token + locks the row.
//  2. Validates the invite is unredeemed and unexpired (sentinel
//     errors for each so the handler can branch on
//     errors.Is).
//  3. Parses the supplied PEM into an ed25519 public key, derives
//     the fingerprint, and upserts a row into authorized_keys.
//  4. Inserts an org_memberships row binding the fingerprint to
//     the invite's org with the invite's role (ON CONFLICT DO
//     NOTHING — re-redeeming into an org the caller is already a
//     member of is idempotent on the membership and just marks
//     the invite redeemed).
//  5. Stamps redeemed_at / redeemed_by on the invite row.
//
// Returns a RedeemResult with the *Inserted flags so the calling
// handler can emit identity.key_registered + org.member.joined
// audit events only when the underlying state actually changed.
func (s *PostgresStore) RedeemInvite(ctx context.Context, token, handle, publicKeyPEM string) (RedeemResult, error) {
	var res RedeemResult
	if token == "" {
		return res, errors.New("server: RedeemInvite requires token")
	}
	if publicKeyPEM == "" {
		return res, errors.New("server: RedeemInvite requires public_key_pem")
	}
	pub, err := identity.ParsePublicPEM([]byte(publicKeyPEM))
	if err != nil {
		return res, fmt.Errorf("server: parse public key: %w", err)
	}
	fp, err := identity.FingerprintOf(pub)
	if err != nil {
		return res, fmt.Errorf("server: derive fingerprint: %w", err)
	}
	res.Fingerprint = fp.String()
	res.Handle = handle

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("server: begin redeem tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // commit overrides on success
	err = func(tx pgx.Tx) error {
		var (
			expiresAt   time.Time
			redeemedAt  *time.Time
			role, orgID string
			inviteID    string
		)
		err := tx.QueryRow(ctx, `
			SELECT id::text, org_id::text, role, expires_at, redeemed_at
			FROM   org_invites
			WHERE  token = $1
			FOR UPDATE
		`, token).Scan(&inviteID, &orgID, &role, &expiresAt, &redeemedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInviteNotFound
			}
			return fmt.Errorf("server: load invite: %w", err)
		}
		if redeemedAt != nil {
			return ErrInviteAlreadyRedeemed
		}
		if !expiresAt.After(time.Now()) {
			return ErrInviteExpired
		}
		res.InviteID = inviteID
		res.OrgID = orgID
		res.Role = role

		keyInserted, err := upsertAuthorizedKeyInTx(ctx, tx,
			res.Fingerprint, handle, publicKeyPEM, "invite-redeem", inviteID,
		)
		if err != nil {
			return err
		}
		res.KeyRegistered = keyInserted

		tag, err := tx.Exec(ctx, `
			INSERT INTO org_memberships (org_id, fingerprint, role)
			VALUES ($1, $2, $3)
			ON CONFLICT (org_id, fingerprint) DO NOTHING
		`, orgID, res.Fingerprint, role)
		if err != nil {
			return fmt.Errorf("server: insert membership: %w", err)
		}
		res.MemberJoined = tag.RowsAffected() == 1

		if _, err := tx.Exec(ctx, `
			UPDATE org_invites
			SET    redeemed_at = now(),
			       redeemed_by = $2
			WHERE  id = $1::uuid
		`, inviteID, res.Fingerprint); err != nil {
			return fmt.Errorf("server: mark invite redeemed: %w", err)
		}
		return nil
	}(tx)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("server: commit redeem tx: %w", err)
	}
	return res, nil
}

// PeekInvite looks up an invite by token without any side effects.
// Backs the GET /invites/<token> handler so the form can fail
// fast on unknown / expired / already-redeemed tokens instead of
// making the recipient paste a PEM first. Sentinel errors mirror
// RedeemInvite so handlers can branch on the same set.
func (s *PostgresStore) PeekInvite(ctx context.Context, token string) (Invite, error) {
	var inv Invite
	if token == "" {
		return inv, errors.New("server: PeekInvite requires token")
	}
	var redeemedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, org_id::text, token, role, invited_by, expires_at,
		       expires_at - INTERVAL '7 days' AS created_at,
		       redeemed_at
		FROM   org_invites
		WHERE  token = $1
	`, token).Scan(
		&inv.ID, &inv.OrgID, &inv.Token, &inv.Role, &inv.InvitedBy,
		&inv.ExpiresAt, &inv.CreatedAt, &redeemedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return inv, ErrInviteNotFound
		}
		return inv, fmt.Errorf("server: peek invite: %w", err)
	}
	if redeemedAt != nil {
		return inv, ErrInviteAlreadyRedeemed
	}
	if !inv.ExpiresAt.After(time.Now()) {
		return inv, ErrInviteExpired
	}
	return inv, nil
}

// ListPendingInvites returns every unredeemed, unexpired invite
// for the org so the /members page can render them alongside
// the current memberships. Ordered by expiration descending so
// the most recently issued lands first.
func (s *PostgresStore) ListPendingInvites(ctx context.Context, orgID string) ([]Invite, error) {
	if orgID == "" {
		return nil, errors.New("server: ListPendingInvites requires orgID")
	}
	var out []Invite
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, org_id::text, token, role, invited_by, expires_at,
			       expires_at - INTERVAL '7 days' AS created_at
			FROM   org_invites
			WHERE  org_id = $1
			AND    redeemed_at IS NULL
			AND    expires_at > now()
			ORDER  BY expires_at DESC
		`, orgID)
		if err != nil {
			return fmt.Errorf("server: list invites: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var inv Invite
			if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
				return fmt.Errorf("server: list invites scan: %w", err)
			}
			out = append(out, inv)
		}
		return rows.Err()
	})
	return out, err
}

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
