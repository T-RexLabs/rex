package web

import (
	"context"
	"net/http"

	internalweb "github.com/asabla/rex/internal/web"
)

// workspaceOverviewData backs workspace_overview.tmpl. Carries
// the workspace.yaml metadata the dashboard header renders, plus
// the at-a-glance stat row + recent-runs preview the local home
// has had since v1 (parity gap closed here so central's
// workspace landing isn't just a sitemap of cards). Card links
// stay static — derived from .OrgID + .WorkspaceID at render
// time, so the handler doesn't pass a per-card list.
type workspaceOverviewData struct {
	centralPageData
	WorkspaceName      string
	WorkspaceState     string
	WorkspaceCreatedAt string
	SpecCount          int
	RunCount           int
	RecentRuns         []internalweb.RunRow
}

// handleWorkspaceOverview is GET /orgs/<org>/workspaces/<ws>.
// Renders a dashboard with cards for each per-workspace surface
// (specs / runs / audit / amendments / search / remotes /
// settings). Previously the workspaces-index "open" link routed
// straight to /specs, skipping the dashboard entirely; this
// handler is the natural landing page when a user enters a
// workspace.
//
// Behaviour mirrors the other workspace-scoped routes: 503 when
// the resolver isn't bound, 403 from requireOrgMember on
// non-members, 404 when the workspace id is unknown. The
// workspace.yaml fields are best-effort — a workspace whose
// content hasn't synced yet still renders with id-only.
func (s *Server) handleWorkspaceOverview(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	wsID := r.PathValue("ws")
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	data := workspaceOverviewData{
		centralPageData: s.pageData(orgID, wsID, "workspace"),
	}
	// Read workspace.yaml for human-friendly metadata. Failure
	// is non-fatal — the page renders with the id only, just
	// like settings does.
	if reader, ok := workspaceYAMLReader(s.opts.Resolver, wsID); ok {
		if raw, err := reader.workspaceYAML(context.Background()); err == nil && raw != "" {
			fields := parseWorkspaceFields(raw)
			data.Workspace.ID = firstNonEmpty(fields.ID, ws.ID)
			data.WorkspaceName = fields.Name
			data.WorkspaceState = fields.State
			data.WorkspaceCreatedAt = fields.CreatedAt
		}
	}
	// At-a-glance stats + recent runs. Best-effort: a failing or
	// unbound projection just leaves its counter / table empty so
	// a fresh deployment with no synced content still 200s.
	if ws.Specs != nil {
		if rows, err := ws.Specs.ListSpecs(); err == nil {
			data.SpecCount = len(rows)
		}
	}
	if ws.Runs != nil {
		if rows, err := ws.Runs.ListRuns(); err == nil {
			data.RunCount = len(rows)
			base := "/orgs/" + orgID + "/workspaces/" + wsID
			for i := range rows {
				rows[i].LinkBase = base
			}
			if len(rows) > 5 {
				rows = rows[:5]
			}
			data.RecentRuns = rows
		}
	}
	s.renderer.Render(w, r, "workspace_overview.tmpl", data)
}
