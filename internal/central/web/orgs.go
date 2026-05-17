package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/rbac"
	internalweb "github.com/asabla/rex/internal/web"
)

// orgPageData backs org_overview.tmpl. Embeds the central page
// envelope so the shared base layout fields resolve.
type orgPageData struct {
	centralPageData
	Org internalweb.OrgSummary
}

// orgMembersData backs org_members.tmpl.
type orgMembersData struct {
	centralPageData
	Members []internalweb.MembershipRow
	// ViewerIsAdmin gates the per-row role-change + remove
	// forms. Non-admin viewers see the membership list without
	// mutation affordances; admins see dropdowns + remove
	// buttons inline (CENTRAL.3 — every mutation runs through
	// RBAC; the page-level check echoes the handler-level one).
	ViewerIsAdmin bool
	// Flash is a one-shot status string rendered above the
	// table after a successful mutation reloads the page.
	Flash string
}

// orgRolesData backs org_roles.tmpl. The role catalog is static
// in v1 — built from internal/core/rbac at handler-call time —
// so a Role list with permission strings is enough.
type orgRolesData struct {
	centralPageData
	Roles []internalweb.RoleCatalogRow
}

// handleOrgOverview is GET /orgs/<org-id>. Renders the org's
// header card + quick-action navigation to the per-org admin
// surfaces. Gated behind the CENTRAL ONLY banner per CENTRAL.2.
func (s *Server) handleOrgOverview(w http.ResponseWriter, r *http.Request) {
	if s.opts.Orgs == nil {
		http.Error(w, "central web: orgs projection not configured (admin API pending — central-node.RBAC-SVR.1)", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	if orgID == "" {
		http.NotFound(w, r)
		return
	}
	org, found, err := s.opts.Orgs.LookupOrg(orgID)
	if err != nil {
		http.Error(w, "central web: lookup org: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	data := orgPageData{
		centralPageData: s.orgPage(orgID, "org"),
		Org:             org,
	}
	s.renderer.Render(w, r, "org_overview.tmpl", data)
}

// handleOrgMembers is GET /orgs/<org-id>/members. Admins see
// per-row role-change + remove forms inline; non-admins see the
// membership list read-only. Both branches gate behind
// requireOrgMember first, so a non-member of org-A still gets
// 403 when reading /orgs/orgA/members regardless of their role
// elsewhere (CENTRAL.3 + identity-and-trust.RBAC.1).
func (s *Server) handleOrgMembers(w http.ResponseWriter, r *http.Request) {
	if s.opts.Orgs == nil {
		http.Error(w, "central web: orgs projection not configured (admin API pending — central-node.RBAC-SVR.1)", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	_, role, ok := s.requireOrgMember(w, r, orgID)
	if !ok {
		return
	}
	if orgID == "" {
		http.NotFound(w, r)
		return
	}
	members, err := s.opts.Orgs.ListMembers(orgID)
	if err != nil {
		http.Error(w, "central web: list members: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := orgMembersData{
		centralPageData: s.orgPage(orgID, "members"),
		Members:         members,
		ViewerIsAdmin:   role == "admin",
		Flash:           r.URL.Query().Get("flash"),
	}
	s.renderer.Render(w, r, "org_members.tmpl", data)
}

// handleOrgMemberRoleChange is POST /orgs/<org-id>/members/<fp>/role.
// Admin-only. Updates the membership's role and redirects back
// to /orgs/<org-id>/members with a one-shot flash. Audit emission
// is a follow-up — the appender + event types are wired but the
// adapter-side emit needs context-threading to capture the
// changer's fingerprint.
func (s *Server) handleOrgMemberRoleChange(w http.ResponseWriter, r *http.Request) {
	if s.opts.Orgs == nil {
		http.Error(w, "central web: orgs projection not configured (admin API pending — central-node.RBAC-SVR.1)", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if !s.requireAdmin(w, r, orgID) {
		return
	}
	fp := r.PathValue("fp")
	if fp == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "central web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	newRole := strings.TrimSpace(r.FormValue("role"))
	if newRole == "" {
		http.Error(w, "central web: role is required", http.StatusBadRequest)
		return
	}
	changer, _ := SessionFromContext(r.Context())
	prior, err := s.opts.Orgs.ChangeMemberRole(orgID, fp, newRole, changer.Fingerprint)
	switch {
	case errors.Is(err, internalweb.ErrUnknownMembership):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, "central web: change role: "+err.Error(), http.StatusInternalServerError)
		return
	}
	flash := "role unchanged"
	if prior != newRole {
		flash = "role changed: " + fp + " " + prior + " -> " + newRole
	}
	http.Redirect(w, r, "/orgs/"+orgID+"/members?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// handleOrgMemberRemove is POST /orgs/<org-id>/members/<fp>/remove.
// Admin-only. Deletes the membership and redirects back to
// /orgs/<org-id>/members with a flash.
func (s *Server) handleOrgMemberRemove(w http.ResponseWriter, r *http.Request) {
	if s.opts.Orgs == nil {
		http.Error(w, "central web: orgs projection not configured (admin API pending — central-node.RBAC-SVR.1)", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if !s.requireAdmin(w, r, orgID) {
		return
	}
	fp := r.PathValue("fp")
	if fp == "" {
		http.NotFound(w, r)
		return
	}
	remover, _ := SessionFromContext(r.Context())
	prior, err := s.opts.Orgs.RemoveMember(orgID, fp, remover.Fingerprint)
	switch {
	case errors.Is(err, internalweb.ErrUnknownMembership):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, "central web: remove member: "+err.Error(), http.StatusInternalServerError)
		return
	}
	flash := "removed " + fp + " (was " + prior + ")"
	http.Redirect(w, r, "/orgs/"+orgID+"/members?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// requireAdmin is the role-gating sibling of requireOrgMember:
// passes only when the caller has admin role in orgID. Returns
// false on failure (after writing the appropriate response) so
// handlers can early-return. The check stacks on top of
// requireSession + requireOrgMember — both have already fired
// because requireAdmin calls into the same RoleFor path.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request, orgID string) bool {
	_, role, ok := s.requireOrgMember(w, r, orgID)
	if !ok {
		return false
	}
	if s.opts.Auth == nil || s.opts.Orgs == nil {
		// Dev-mode pass-through (matches requireOrgMember's
		// shape). Production deployments always have both bound.
		return true
	}
	if role != "admin" {
		http.Error(w, "forbidden: admin role required", http.StatusForbidden)
		return false
	}
	return true
}

// handleOrgRoles is GET /orgs/<org-id>/roles. The catalog is
// derived from internal/core/rbac at request time — v1 ships
// the built-in roles (admin/member/viewer); per-org custom roles
// land with the admin API.
func (s *Server) handleOrgRoles(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	if orgID == "" {
		http.NotFound(w, r)
		return
	}
	data := orgRolesData{
		centralPageData: s.orgPage(orgID, "roles"),
		Roles:           builtinRoleCatalog(),
	}
	s.renderer.Render(w, r, "org_roles.tmpl", data)
}

// orgPage assembles the centralPageData envelope for org-scoped
// admin surfaces. CentralOnly is true so the shared base layout
// renders the "CENTRAL ONLY" banner above the page (CENTRAL.2).
func (s *Server) orgPage(orgID, nav string) centralPageData {
	return centralPageData{
		BindAddr:    s.opts.BindAddr,
		Version:     s.opts.Version,
		NavSection:  nav,
		OrgID:       orgID,
		CentralOnly: true,
	}
}

// builtinRoleCatalog projects internal/core/rbac's built-in role
// table into the page-friendly shape. Sort permissions inside
// each role so the rendered list is deterministic across builds.
func builtinRoleCatalog() []internalweb.RoleCatalogRow {
	roles := []rbac.Role{rbac.RoleAdmin, rbac.RoleMember, rbac.RoleViewer}
	out := make([]internalweb.RoleCatalogRow, 0, len(roles))
	for _, r := range roles {
		perms, _ := rbac.RolePermissions(r)
		strs := make([]string, 0, len(perms))
		for p := range perms {
			strs = append(strs, string(p))
		}
		sort.Strings(strs)
		out = append(out, internalweb.RoleCatalogRow{
			Role:        strings.ToLower(string(r)),
			Permissions: strs,
		})
	}
	return out
}
