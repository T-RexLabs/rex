package server

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// SessionCookieName is the HttpOnly cookie that carries the
// browser's bearer token on the central web UI (web-ui.CENTRAL-AUTH.1).
const SessionCookieName = "rex_session"

const (
	authRedeemPath = "/auth/redeem"
	authLogoutPath = "/auth/logout"
)

// IssueLoginChallenge issues a fresh challenge for browser login.
// Returns the proto-level package (challenge id + nonce + hostname
// + expiry; redirect is left to the caller to stamp). The web
// shell calls this from /login (web-ui.CENTRAL-AUTH.2), sets the
// redirect field from the request, and renders the encoded form
// for the user to copy into `rex remote login --challenge <s>`.
func (s *Server) IssueLoginChallenge(hostname string) (proto.LoginChallengePackage, error) {
	c, err := s.auth.issueChallenge(hostname)
	if err != nil {
		return proto.LoginChallengePackage{}, err
	}
	return proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: c.id,
		Nonce:       hex.EncodeToString(c.nonce),
		Hostname:    c.hostname,
		ExpiresAt:   c.expiresAt,
	}, nil
}

// ResolveBearer validates a bearer token (from Authorization or
// rex_session) and returns the bound fingerprint plus its expiry.
// Returns a non-nil error when the token is unknown, expired, or
// revoked. Pure read; safe for concurrent use.
func (s *Server) ResolveBearer(value string) (identity.Fingerprint, time.Time, error) {
	tok, err := s.auth.resolveAccessToken(value)
	if err != nil {
		return identity.Fingerprint{}, time.Time{}, err
	}
	return tok.fingerprint, tok.expiresAt, nil
}

// RevokeBearer invalidates the token presented at logout. Returns
// nil on success (including the idempotent "already revoked" path),
// non-nil on internal failures.
func (s *Server) RevokeBearer(value string) error {
	_, err := s.auth.revokeToken(value)
	return err
}

// tokenFromRequest returns the bearer value carried by r. It prefers
// the Authorization header (API consumers) and falls back to the
// rex_session cookie (browser consumers) per web-ui.CENTRAL-AUTH.3.
// Returns ("", false) when neither source is present.
func tokenFromRequest(r *http.Request) (string, bool) {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, bearerPrefix) {
		return strings.TrimPrefix(header, bearerPrefix), true
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// handleAuthRedeem is GET /auth/redeem — the browser-targeted
// landing for the login flow (web-ui.CENTRAL-AUTH.2). The CLI
// directs the browser here with ?token=<access_token>&redirect=<path>
// after a successful /auth/verify; the handler validates the
// token, sets the rex_session cookie, and 302s to redirect.
//
// Failure modes:
//   - method not GET → 405
//   - missing or empty token → 400
//   - token does not resolve (expired / revoked / unknown) → 401
//   - redirect path is absolute or schemeful → 400 (open-redirect
//     hardening; we only honour same-origin paths starting with "/")
func (s *Server) handleAuthRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET only")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "token query parameter is required")
		return
	}
	tok, err := s.auth.resolveAccessToken(token)
	if err != nil {
		s.log.Warn("auth redeem: token resolve failed",
			"op", "auth.redeem",
			"err", err.Error(),
		)
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "token invalid")
		return
	}

	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	if !isSafeRedirect(redirect) {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "redirect must be a same-origin path")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  tok.expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	s.log.Info("auth redeem: cookie set",
		"op", "auth.redeem",
		"fingerprint", tok.fingerprint.String(),
		"redirect", redirect,
	)
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// handleAuthLogout is POST /auth/logout — clears rex_session and
// invalidates the underlying token (web-ui.CENTRAL-AUTH.4). The
// cookie is cleared even on revoke errors so a corrupted token in
// the cookie can't pin the session open.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "POST only")
		return
	}
	c, err := r.Cookie(SessionCookieName)
	if err == nil && c.Value != "" {
		if _, rerr := s.auth.revokeToken(c.Value); rerr != nil {
			// Log but don't fail the request — clearing the cookie
			// is the authoritative side-effect from the browser's
			// perspective.
			s.log.Info("auth logout: revoke skipped",
				"op", "auth.logout",
				"err", rerr.Error(),
			)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// isSafeRedirect returns true for paths that point at the same
// origin: must start with "/" and not be a protocol-relative URL
// ("//host/path") or contain an authority. Rejects schemeful URLs
// outright. Defends against open-redirect abuse on the /auth/redeem
// surface.
func isSafeRedirect(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") {
		return false
	}
	if strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		return false
	}
	u, err := url.Parse(path)
	if err != nil {
		return false
	}
	if u.Scheme != "" || u.Host != "" {
		return false
	}
	return true
}
