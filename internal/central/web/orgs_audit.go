package web

import (
	"net/http"
	"strconv"

	internalweb "github.com/asabla/rex/internal/web"
)

// orgAuditData backs org_audit.tmpl. Mirrors centralAuditData so
// the audit_row partial renders identically; the extra OrgID
// field surfaces in the page header (which org's events are
// being tailed).
type orgAuditData struct {
	centralPageData
	Rows   []internalweb.AuditRow
	Limit  int
	Source string
}

// handleOrgAudit is GET /orgs/<org-id>/audit. Surfaces audit
// events tied to the org — org.member.invited/joined/role_changed/
// removed, identity.key_registered, remote.attached/detached
// (when their workspaces belong to the org), auth.* / token.* —
// without descending into a specific workspace. CENTRAL.3
// (every action runs through RBAC and writes audit entries)
// implies audit visibility per org.
//
// 503 when no OrgAudit projection is bound (dev-mode without
// --db). The page gates on org membership the same way the
// other /orgs/<id>/... routes do.
func (s *Server) handleOrgAudit(w http.ResponseWriter, r *http.Request) {
	if s.opts.OrgAudit == nil {
		http.Error(w, "central web: org audit not configured (requires --db)", http.StatusServiceUnavailable)
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
	limit := internalweb.AuditDefaultLimit
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 9999 {
			limit = parsed
		}
	}
	rows, err := s.opts.OrgAudit.TailOrgAudit(orgID, limit)
	if err != nil {
		http.Error(w, "central web: tail org audit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := orgAuditData{
		centralPageData: s.orgPage(orgID, "audit"),
		Rows:            rows,
		Limit:           limit,
		Source:          "central event store (org " + orgID + ")",
	}
	s.renderer.Render(w, r, "org_audit.tmpl", data)
}
