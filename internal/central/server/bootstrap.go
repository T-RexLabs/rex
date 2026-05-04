package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BootstrapToken is the read-side projection of an admin_bootstrap
// row. v1 has at most one — the row seeded on first migration.
type BootstrapToken struct {
	ID         string
	Token      string
	CreatedAt  time.Time
	RedeemedAt *time.Time
	RedeemedBy string
}

// Pending reports whether the token still has a chance to be
// redeemed. False once redeemed.
func (b BootstrapToken) Pending() bool { return b.RedeemedAt == nil }

// LookupBootstrapToken returns the singleton admin_bootstrap row,
// or nil + false when no token has been seeded (which means an
// admin already exists — bootstrap mode is over).
func (s *PostgresStore) LookupBootstrapToken(ctx context.Context) (*BootstrapToken, bool, error) {
	var (
		t   BootstrapToken
		ra  *time.Time
		rby *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, token, created_at, redeemed_at, redeemed_by
		FROM   admin_bootstrap
		ORDER  BY created_at
		LIMIT  1
	`).Scan(&t.ID, &t.Token, &t.CreatedAt, &ra, &rby)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("server: lookup bootstrap token: %w", err)
	}
	t.RedeemedAt = ra
	if rby != nil {
		t.RedeemedBy = *rby
	}
	return &t, true, nil
}

// RedeemBootstrapToken atomically marks the token as redeemed
// by `fingerprint` and upgrades that fingerprint's default-org
// membership to role='admin'. The redemption is a single
// transaction so a partial state never leaks: either the token
// gets stamped AND the role flip lands, or nothing changes.
//
// Returns ErrBootstrapTokenInvalid when the token doesn't match
// or has already been redeemed. Returns ErrBootstrapNotMember
// when the redeemer isn't yet a member of the default org —
// the auth-verify auto-join hook normally lands them there
// before redemption, so seeing this error means a misordered
// flow.
func (s *PostgresStore) RedeemBootstrapToken(ctx context.Context, token, fingerprint string) error {
	if token == "" {
		return ErrBootstrapTokenInvalid
	}
	if fingerprint == "" {
		return errors.New("server: empty fingerprint")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("server: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // commit overrides

	var id string
	err = tx.QueryRow(ctx, `
		UPDATE admin_bootstrap
		SET    redeemed_at = now(),
		       redeemed_by = $2
		WHERE  token = $1
		AND    redeemed_at IS NULL
		RETURNING id::text
	`, token, fingerprint).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBootstrapTokenInvalid
		}
		return fmt.Errorf("server: redeem bootstrap: %w", err)
	}

	// Upgrade the membership row to admin. UPDATE returns 0
	// rows when no membership exists yet — that's the
	// misordered-flow case, surface it explicitly so the
	// caller can guide the user.
	tag, err := tx.Exec(ctx, `
		UPDATE org_memberships m
		SET    role = 'admin'
		FROM   orgs o
		WHERE  m.org_id = o.id
		AND    o.name = $1
		AND    m.fingerprint = $2
	`, DefaultOrgName, fingerprint)
	if err != nil {
		return fmt.Errorf("server: upgrade to admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBootstrapNotMember
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("server: commit redeem: %w", err)
	}
	return nil
}

// AnyAdminExists reports whether any org_memberships row has
// role='admin'. The server's startup check uses this to decide
// whether to log the bootstrap token (BOOT.1).
func (s *PostgresStore) AnyAdminExists(ctx context.Context) (bool, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM org_memberships WHERE role = 'admin' LIMIT 1`,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("server: any admin exists: %w", err)
	}
	return n > 0, nil
}

var (
	// ErrBootstrapTokenInvalid covers both "no such token" and
	// "already redeemed" — same outward shape so the error
	// doesn't leak which case fired (AUTH.3 spirit applied to
	// admin claim).
	ErrBootstrapTokenInvalid = errors.New("server: bootstrap token invalid or already redeemed")

	// ErrBootstrapNotMember fires when the redeemer hasn't yet
	// been auto-joined to the default org. Returned distinctly
	// from ErrBootstrapTokenInvalid so the CLI can hint at
	// "run rex remote test first" rather than implying the
	// token is wrong.
	ErrBootstrapNotMember = errors.New("server: redeemer is not a member of the default org")
)
