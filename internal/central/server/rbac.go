package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/asabla/rex/internal/core/rbac"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// requirePermission is the server-side RBAC gate (identity-and-trust.
// RBAC.1). It loads the role the (orgID, fingerprint) pair holds via
// the Store's RoleResolver implementation, builds a Grant, and asks
// rbac.Allow.
//
// Returns nil when the request is allowed OR when no RBAC enforcement
// is configured (the in-memory MemoryStore path that backs dev/test
// — same bypass principle as the keystore-empty signature path).
// Returns a *rbacDeniedError otherwise.
//
// The wsID, harness, and tool fields narrow the request when the
// caller has them. Empty strings mean "not applicable / not bound";
// rbac.Allow ignores constraint dimensions that the request omits.
func (s *Server) requirePermission(ctx context.Context, fingerprint, orgID string, action rbac.Permission, wsID, harness, tool string) error {
	resolver, ok := s.store.(RoleResolver)
	if !ok {
		// No RBAC layer wired (MemoryStore in dev/test). Pass.
		return nil
	}
	if fingerprint == "" || orgID == "" {
		// Auth/tenant gate didn't resolve an identity — defer to
		// the existing 401/403 path that fires before this gate.
		return nil
	}
	role, err := resolver.RoleFor(ctx, orgID, fingerprint)
	if err != nil {
		return err
	}
	if role == "" {
		// Identity has no membership in this org. Deny without
		// checking the catalog so the message is descriptive.
		return &rbacDeniedError{
			action: action,
			reason: "no membership in org",
		}
	}
	d := rbac.Allow(rbac.Request{
		Fingerprint: fingerprint,
		OrgID:       orgID,
		Action:      action,
		Workspace:   wsID,
		Harness:     harness,
		Tool:        tool,
	}, []rbac.Grant{{
		Fingerprint: fingerprint,
		OrgID:       orgID,
		Role:        rbac.Role(role),
	}})
	if !d.Allowed {
		return &rbacDeniedError{action: action, reason: d.Reason}
	}
	return nil
}

// rbacDeniedError is the typed error requirePermission returns on a
// deny. Carries the action and the human-readable reason so the
// handler can shape a 403 with both pieces.
type rbacDeniedError struct {
	action rbac.Permission
	reason string
}

func (e *rbacDeniedError) Error() string {
	return string(e.action) + " denied: " + e.reason
}

// writeRBACDenied is the standardized 403 response for an
// rbacDeniedError. Logs the deny at INFO with a structured field set
// (no payloads, no signatures).
func (s *Server) writeRBACDenied(w http.ResponseWriter, _ *http.Request, err error) {
	var denied *rbacDeniedError
	if !errors.As(err, &denied) {
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	s.log.Info("rbac deny",
		"op", "rbac",
		"action", string(denied.action),
		"reason", denied.reason,
	)
	writeError(w, http.StatusForbidden, "permission_denied", denied.Error())
}
