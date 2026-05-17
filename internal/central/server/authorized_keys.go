package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/asabla/rex/internal/core/identity"
)

// AuthorizedKeyRecord is one row in the authorized_keys table
// (schema step 8). Mirrors the runtime-registered keys named in
// identity-and-trust.AUTH.2.2 — currently only the invite-redeem
// flow produces them, but the Source field leaves room for an
// admin-paste path later.
//
// PublicKeyPEM is the same wire shape the TOML --keys file uses
// so the in-memory Keystore can load DB + file rows without two
// parse paths.
type AuthorizedKeyRecord struct {
	Fingerprint  string
	Handle       string
	PublicKeyPEM string
	Source       string
	InviteID     string
	RegisteredAt time.Time
}

// RegisterAuthorizedKey upserts a row in authorized_keys. The
// inserted flag reports whether the INSERT actually fired (true)
// or whether the fingerprint was already known (false). Callers
// use it to gate audit emission — re-redeeming a key that's
// already registered doesn't re-emit identity.key_registered.
//
// inviteID may be empty for non-invite paths; the underlying
// column is nullable.
func (s *PostgresStore) RegisterAuthorizedKey(
	ctx context.Context,
	fingerprint, handle, pem, source, inviteID string,
) (inserted bool, err error) {
	if fingerprint == "" || pem == "" || source == "" {
		return false, errors.New("server: RegisterAuthorizedKey requires fingerprint + pem + source")
	}
	var inviteArg any
	if inviteID == "" {
		inviteArg = nil
	} else {
		inviteArg = inviteID
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO authorized_keys (fingerprint, handle, public_key_pem, source, invite_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (fingerprint) DO NOTHING
	`, fingerprint, handle, pem, source, inviteArg)
	if err != nil {
		return false, fmt.Errorf("server: register authorized key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListAuthorizedKeys returns every row in authorized_keys ordered
// by fingerprint. Called once at startup by
// LoadAuthorizedKeysIntoKeystore to overlay DB-registered keys on
// top of the TOML --keys load.
func (s *PostgresStore) ListAuthorizedKeys(ctx context.Context) ([]AuthorizedKeyRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint, handle, public_key_pem, source,
		       COALESCE(invite_id::text, ''), registered_at
		FROM   authorized_keys
		ORDER  BY fingerprint
	`)
	if err != nil {
		return nil, fmt.Errorf("server: list authorized keys: %w", err)
	}
	defer rows.Close()
	var out []AuthorizedKeyRecord
	for rows.Next() {
		var r AuthorizedKeyRecord
		if err := rows.Scan(&r.Fingerprint, &r.Handle, &r.PublicKeyPEM, &r.Source, &r.InviteID, &r.RegisteredAt); err != nil {
			return nil, fmt.Errorf("server: scan authorized key: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadAuthorizedKeysIntoKeystore reads every row from the
// authorized_keys table and overlays it onto ks. Called once at
// startup, after the TOML --keys file has been loaded — so the DB
// rows take precedence on fingerprint conflict, per AUTH.2.2.
//
// Parse failures on a single row are logged through the supplied
// logger (when non-nil) and skipped — a malformed DB row should
// not prevent the central node from starting, since the TOML
// keystore can still cover bootstrap auth. The returned count
// reports successful overlays.
func LoadAuthorizedKeysIntoKeystore(ctx context.Context, ks *Keystore, store interface {
	ListAuthorizedKeys(ctx context.Context) ([]AuthorizedKeyRecord, error)
}, logger *slog.Logger) (int, error) {
	if ks == nil {
		return 0, errors.New("server: LoadAuthorizedKeysIntoKeystore requires keystore")
	}
	if store == nil {
		return 0, errors.New("server: LoadAuthorizedKeysIntoKeystore requires store")
	}
	rows, err := store.ListAuthorizedKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("server: load authorized keys: %w", err)
	}
	loaded := 0
	for _, r := range rows {
		pub, err := identity.ParsePublicPEM([]byte(r.PublicKeyPEM))
		if err != nil {
			if logger != nil {
				logger.Warn("authorized_keys row failed PEM parse",
					"op", "startup",
					"fingerprint", r.Fingerprint,
					"handle", r.Handle,
					"err", err.Error(),
				)
			}
			continue
		}
		fp, err := identity.FingerprintOf(pub)
		if err != nil {
			if logger != nil {
				logger.Warn("authorized_keys row failed fingerprint derivation",
					"op", "startup",
					"fingerprint", r.Fingerprint,
					"err", err.Error(),
				)
			}
			continue
		}
		if fp.String() != r.Fingerprint {
			if logger != nil {
				logger.Warn("authorized_keys row fingerprint mismatch",
					"op", "startup",
					"declared", r.Fingerprint,
					"derived", fp.String(),
				)
			}
			continue
		}
		if _, err := ks.Add(r.Handle, pub); err != nil {
			if logger != nil {
				logger.Warn("authorized_keys row failed keystore add",
					"op", "startup",
					"fingerprint", r.Fingerprint,
					"err", err.Error(),
				)
			}
			continue
		}
		loaded++
	}
	return loaded, nil
}

// pgxTxIface is a tiny interface so RedeemInvite can run the
// authorized_keys upsert inside an outer transaction (rather than
// the s.pool.Exec on RegisterAuthorizedKey, which would commit
// independently and stay registered even if the rest of the
// redeem rolls back).
func upsertAuthorizedKeyInTx(
	ctx context.Context, tx pgx.Tx,
	fingerprint, handle, pem, source, inviteID string,
) (bool, error) {
	var inviteArg any
	if inviteID == "" {
		inviteArg = nil
	} else {
		inviteArg = inviteID
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO authorized_keys (fingerprint, handle, public_key_pem, source, invite_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (fingerprint) DO NOTHING
	`, fingerprint, handle, pem, source, inviteArg)
	if err != nil {
		return false, fmt.Errorf("server: tx upsert authorized key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
