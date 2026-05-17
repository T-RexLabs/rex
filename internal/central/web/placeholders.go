package web

import "net/http"

// centralPlaceholderData backs placeholder_central.tmpl. Used by
// the two v1 shell-only org-scoped surfaces: /orgs/<id>/idp and
// /orgs/<id>/encryption-keys. Both render a friendly "deferred"
// page with a tracking hint, gated behind the CENTRAL ONLY
// banner (web-ui.CENTRAL.2 + amendment 2026-05-15).
type centralPlaceholderData struct {
	centralPageData
	Title        string
	Body         string
	TrackingHint string
}

// handleOrgIdP is GET /orgs/<org-id>/idp. The underlying SSO/IdP
// bridging is itself deferred per central-node.IDP-CENTRAL.1-note;
// this page exists to make the eventual route discoverable and
// to keep the nav shape stable for users (web-ui.CENTRAL.1).
func (s *Server) handleOrgIdP(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	if orgID == "" {
		http.NotFound(w, r)
		return
	}
	data := centralPlaceholderData{
		centralPageData: s.placeholderPage(orgID, "idp"),
		Title:           "identity provider",
		Body: "Single sign-on bridging is deferred. The org table already " +
			"holds optional idp_config + scim_config fields ready for the " +
			"eventual implementation, but no bridging code is live yet — " +
			"see central-node.IDP-CENTRAL.1-note for the deferral rationale.",
		TrackingHint: "central-node.IDP-CENTRAL.1-note",
	}
	s.renderer.Render(w, r, "placeholder_central.tmpl", data)
}

// handleOrgEncryptionKeys is GET /orgs/<org-id>/encryption-keys.
// Per-tenant transcript encryption is deferred to v1.5
// (storage.encryption-opt-in); the page exists today as the
// eventual surface and explains the deferral inline.
func (s *Server) handleOrgEncryptionKeys(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	if orgID == "" {
		http.NotFound(w, r)
		return
	}
	data := centralPlaceholderData{
		centralPageData: s.placeholderPage(orgID, "encryption-keys"),
		Title:           "encryption keys",
		Body: "Per-tenant transcript encryption is deferred to v1.5. The " +
			"data model + key-management surface land alongside central-side " +
			"execution (overview.SCOPE.1); this placeholder will host the " +
			"key rotation UI when that work ships.",
		TrackingHint: "storage.encryption-opt-in + web-ui.CENTRAL.4",
	}
	s.renderer.Render(w, r, "placeholder_central.tmpl", data)
}

// placeholderPage assembles the centralPageData envelope for the
// org-scoped shell-only pages. CentralOnly is set so the shared
// base layout renders the "CENTRAL ONLY" banner above the page
// content (web-ui.CENTRAL.2).
func (s *Server) placeholderPage(orgID, nav string) centralPageData {
	return centralPageData{
		BindAddr:    s.opts.BindAddr,
		Version:     s.opts.Version,
		NavSection:  nav,
		OrgID:       orgID,
		CentralOnly: true,
		Shell:       "central",
	}
}
