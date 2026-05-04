package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// BootstrapTokenAccessor is the optional interface a Store must
// satisfy to support the admin-bootstrap flow. PostgresStore
// implements it; MemoryStore does not — the in-memory dev/test
// path is single-tenant and has no admin concept.
//
// Same opt-in shape as Pinger / MembershipEnsurer / MembershipLister.
type BootstrapTokenAccessor interface {
	LookupBootstrapToken(ctx context.Context) (*BootstrapToken, bool, error)
	RedeemBootstrapToken(ctx context.Context, token, fingerprint string) error
	AnyAdminExists(ctx context.Context) (bool, error)
}

// adminBootstrapPath is the canonical URL for the redeem call.
const adminBootstrapPath = "/admin/bootstrap"

// handleAdminBootstrap is POST /admin/bootstrap (BOOT.2). The
// caller must already hold a /auth/verify-issued bearer token
// so the redeemer's fingerprint is known. The body carries the
// one-time admin claim token; on success the requester's
// default-org membership is upgraded to admin and the token is
// invalidated.
//
// Failure modes:
//   - keystore is empty (dev mode) → 503: bootstrap requires
//     an authenticated identity, which requires a configured
//     keystore. Mismatched dev configurations should fail
//     loudly rather than silently grant admin to an
//     unauthenticated client.
//   - missing/invalid token → 401 (single error shape, doesn't
//     leak whether token was wrong vs already redeemed).
//   - redeemer never auth-verified through the auto-join hook
//     → 409 (suggests the user run a normal authentication
//     dance first to land in the default org).
//   - store doesn't support bootstrap (MemoryStore) → 503.
func (s *Server) handleAdminBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "POST only")
		return
	}
	if s.keystore.Empty() {
		writeError(w, http.StatusServiceUnavailable, "bootstrap_unauthenticated_mode",
			"bootstrap requires an authenticated identity; configure --keys")
		return
	}
	fp, err := s.requireToken(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, err.Error())
		return
	}

	accessor, ok := s.store.(BootstrapTokenAccessor)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "bootstrap_unsupported",
			"this server's event store does not support admin bootstrap (use postgres)")
		return
	}

	var req proto.BootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode: "+err.Error())
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "token is required")
		return
	}

	if err := accessor.RedeemBootstrapToken(r.Context(), req.Token, fp.String()); err != nil {
		switch {
		case errors.Is(err, ErrBootstrapTokenInvalid):
			s.log.Warn("admin bootstrap: invalid token",
				"op", "admin.bootstrap",
				"fingerprint", fp.String(),
			)
			writeError(w, http.StatusUnauthorized, "bootstrap_invalid_token", err.Error())
		case errors.Is(err, ErrBootstrapNotMember):
			writeError(w, http.StatusConflict, "bootstrap_not_member", err.Error())
		default:
			s.log.Error("admin bootstrap: server error", "op", "admin.bootstrap", "err", err)
			writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		}
		return
	}

	// Resolve the default org for the response body. Best-effort
	// lookup: the redeem succeeded so the org definitely exists.
	defaultOrg := proto.BootstrapResponse{Fingerprint: fp.String()}
	if pgStore, ok := s.store.(*PostgresStore); ok {
		if org, lerr := pgStore.LookupOrg(r.Context(), DefaultOrgName); lerr == nil {
			defaultOrg.OrgID = org.ID
			defaultOrg.OrgName = org.Name
		}
	}

	s.log.Info("admin bootstrap: token redeemed",
		"op", "admin.bootstrap",
		"fingerprint", fp.String(),
		"org_id", defaultOrg.OrgID,
	)
	writeJSON(w, http.StatusOK, defaultOrg)
}

// announceBootstrapToken is called once at startup (after the
// store is opened + before the HTTP listener accepts traffic).
// When no admin exists, it logs the seeded token at WARN level
// AND writes it to a host-filesystem file at tokenPath. When
// an admin exists, it deletes the on-disk file (cleanup after
// successful redemption) and logs nothing.
//
// tokenPath is the operator-configured location; bundled
// docker-compose mounts /var/lib/rex (the rex-state volume)
// so /var/lib/rex/bootstrap.token is the canonical path.
// Empty tokenPath skips the file write (logs-only mode for
// bare-metal deployments that don't want a file dropped).
func announceBootstrapToken(ctx context.Context, store Store, tokenPath string, log adminLogger) {
	accessor, ok := store.(BootstrapTokenAccessor)
	if !ok {
		return // MemoryStore — bootstrap is a Postgres-only feature.
	}
	hasAdmin, err := accessor.AnyAdminExists(ctx)
	if err != nil {
		log.Warn("admin bootstrap: admin check failed", "op", "startup", "err", err.Error())
		return
	}
	tok, exists, err := accessor.LookupBootstrapToken(ctx)
	if err != nil {
		log.Warn("admin bootstrap: token lookup failed", "op", "startup", "err", err.Error())
		return
	}
	if hasAdmin {
		// Bootstrap mode is over. Clean up the file if it's
		// still on disk from before redemption.
		if tokenPath != "" {
			if rerr := os.Remove(tokenPath); rerr != nil && !os.IsNotExist(rerr) {
				log.Warn("admin bootstrap: cleanup token file failed",
					"op", "startup",
					"path", tokenPath,
					"err", rerr.Error(),
				)
			}
		}
		return
	}
	if !exists || !tok.Pending() {
		// No token row + no admin = a degenerate state (the
		// migration should always seed one). Log loudly so an
		// operator notices — but don't crash the server, the
		// existing flow can still proceed once an admin is
		// created some other way.
		log.Warn("admin bootstrap: no admin and no pending token",
			"op", "startup",
			"hint", "the migration should seed a token; check the rex_schema_version table",
		)
		return
	}

	log.Warn("admin bootstrap: claim this central node by redeeming the token below",
		"op", "startup.bootstrap",
		"token", tok.Token,
		"persisted_to", tokenPath,
	)
	if tokenPath != "" {
		if err := os.MkdirAll(filepath.Dir(tokenPath), 0o755); err != nil {
			log.Warn("admin bootstrap: mkdir for token file failed",
				"op", "startup", "path", tokenPath, "err", err.Error())
			return
		}
		if err := os.WriteFile(tokenPath, []byte(tok.Token+"\n"), 0o600); err != nil {
			log.Warn("admin bootstrap: write token file failed",
				"op", "startup", "path", tokenPath, "err", err.Error())
		}
	}
}

// adminLogger is the slog-shaped subset announceBootstrapToken
// needs. Defined locally so the helper doesn't import slog
// directly — the server's *slog.Logger satisfies it.
type adminLogger interface {
	Warn(msg string, args ...any)
}
