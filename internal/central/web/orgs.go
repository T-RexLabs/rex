package web

import (
	"net/http"
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

// handleOrgMembers is GET /orgs/<org-id>/members. Read-only on
// central in v1; the page carries an inline notice pointing at
// central-node.RBAC-SVR.1 for the mutation surface.
func (s *Server) handleOrgMembers(w http.ResponseWriter, r *http.Request) {
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
	members, err := s.opts.Orgs.ListMembers(orgID)
	if err != nil {
		http.Error(w, "central web: list members: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := orgMembersData{
		centralPageData: s.orgPage(orgID, "members"),
		Members:         members,
	}
	s.renderer.Render(w, r, "org_members.tmpl", data)
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
