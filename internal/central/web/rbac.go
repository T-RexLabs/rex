package web

import (
	"net/http"
)

// requireOrgMember enforces that the request's authenticated
// identity belongs to the org named in the {org} path segment.
// Used by every /orgs/<org>/... handler — including workspace-
// scoped routes — so a valid session token for org-A can't read
// org-B's content by typing the URL.
//
// Returns the resolved orgID + role on success. On failure
// writes the appropriate status to w and returns ok=false; the
// handler should simply return.
//
// Pass-through branches (preserves the v1 dev-mode shape):
//
//   - Auth == nil: the gate is off, so no SessionInfo was
//     stashed. Return ("", "", true) so the handler renders
//     without checks — matches the rest-of-page behaviour when
//     the dev shell skips auth.
//   - Orgs == nil: the gate validated identity but no
//     membership store is bound (--keys without --db). Same
//     pass-through — there's nothing to check against. A future
//     RBAC tightening can flip this to 503 once every
//     production deployment carries Orgs.
//
// Enforcement branches:
//
//   - missing session on context (gate is on but caller bypassed
//     it somehow) → 401, ok=false.
//   - empty orgID in path → 404 (no org named "" exists).
//   - RoleFor returns "" → 403, ok=false. The body intentionally
//     does NOT distinguish "org doesn't exist" from "you're not
//     a member" — same shape so an unauthorized request can't
//     enumerate orgs.
//   - storage failure on RoleFor → 500, ok=false.
func (s *Server) requireOrgMember(w http.ResponseWriter, r *http.Request, orgID string) (resolvedOrgID, role string, ok bool) {
	if s.opts.Auth == nil || s.opts.Orgs == nil {
		// Dev-mode pass-through. Same convention the session gate
		// uses — nothing wired means nothing enforced.
		return orgID, "", true
	}
	if orgID == "" {
		http.NotFound(w, r)
		return "", "", false
	}
	info, present := SessionFromContext(r.Context())
	if !present {
		// The gate would have stamped this when Auth is set.
		// Reaching this branch means the handler was mounted
		// outside the gate; treat as unauthorised.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", "", false
	}
	r2, err := s.opts.Orgs.RoleFor(orgID, info.Fingerprint)
	if err != nil {
		http.Error(w, "central web: rbac check: "+err.Error(), http.StatusInternalServerError)
		return "", "", false
	}
	if r2 == "" {
		// Membership denial — fold "no such org" into the same
		// 403 so an attacker can't enumerate org ids.
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", "", false
	}
	return orgID, r2, true
}
