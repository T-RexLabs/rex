package web

import (
	"context"
	"net/http"

	internalweb "github.com/asabla/rex/internal/web"
)

// GitWorkspacesLister is the opt-in subset of the central
// GitStore that exposes the set of workspace ids the store
// currently holds content for. MemoryGitStore implements it
// directly; the future PostgresGitStore will satisfy it by
// querying the workspaces table.
//
// Kept separate from GitEntityReader because per-entity reads
// are always workspace-scoped, while the index is the
// not-workspace-scoped pivot point that lets us enumerate them.
type GitWorkspacesLister interface {
	ListWorkspaces() []string
}

// centralWorkspacesIndexProjection satisfies
// internalweb.WorkspacesIndexProjection by enumerating the
// GitStore's workspaces and reading each workspace.yaml for the
// rendered metadata. v1 limitation: the orgID is currently
// ignored because the in-memory GitStore has no org binding;
// when the PostgresGitStore lands the listing tightens to "ws
// rows whose org_id matches orgID".
type centralWorkspacesIndexProjection struct {
	store  GitEntityReader
	lister GitWorkspacesLister
	ctx    context.Context
}

func newCentralWorkspacesIndexProjection(ctx context.Context, store GitEntityReader, lister GitWorkspacesLister) centralWorkspacesIndexProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralWorkspacesIndexProjection{store: store, lister: lister, ctx: ctx}
}

func (p centralWorkspacesIndexProjection) ListWorkspaces(orgID string) ([]internalweb.WorkspaceIndexRow, error) {
	if p.store == nil || p.lister == nil {
		return nil, nil
	}
	ids := p.lister.ListWorkspaces()
	out := make([]internalweb.WorkspaceIndexRow, 0, len(ids))
	for _, id := range ids {
		row := internalweb.WorkspaceIndexRow{ID: id}
		// Best-effort: a workspace that has no workspace.yaml
		// synced yet (events arrived but content didn't) still
		// appears in the index with its id only.
		if rec, err := p.store.Get(p.ctx, id, "workspace.yaml"); err == nil {
			fields := parseWorkspaceFields(rec.Content)
			row.Name = fields.Name
			row.State = fields.State
		}
		out = append(out, row)
	}
	return out, nil
}

// centralWorkspacesIndexData backs workspaces_index.tmpl. OrgID
// rides through so the per-row links can build
// /orgs/<org-id>/workspaces/<ws-id>/specs URLs.
type centralWorkspacesIndexData struct {
	centralPageData
	Workspaces []internalweb.WorkspaceIndexRow
}

// handleWorkspacesIndex is GET /orgs/<org-id>/workspaces. The
// index is org-scoped — there's no ws-id in the URL — so the
// handler reaches into the central resolver's GitStore directly
// and pivots through the GitWorkspacesLister opt-in interface.
func (s *Server) handleWorkspacesIndex(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if orgID == "" {
		http.NotFound(w, r)
		return
	}
	cr, ok := s.opts.Resolver.(centralWorkspaceResolver)
	if !ok || cr.git == nil {
		http.Error(w, "central web: workspaces index requires a GitStore-backed resolver", http.StatusServiceUnavailable)
		return
	}
	lister, ok := cr.git.(GitWorkspacesLister)
	if !ok {
		http.Error(w, "central web: GitStore does not implement GitWorkspacesLister", http.StatusServiceUnavailable)
		return
	}
	rows, err := newCentralWorkspacesIndexProjection(context.Background(), cr.git, lister).ListWorkspaces(orgID)
	if err != nil {
		http.Error(w, "central web: list workspaces: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := centralWorkspacesIndexData{
		centralPageData: centralPageData{
			Workspace:   nil,
			BindAddr:    s.opts.BindAddr,
			Version:     s.opts.Version,
			NavSection:  "workspaces",
			OrgID:       orgID,
			WorkspaceID: "",
		},
		Workspaces: rows,
	}
	s.renderer.Render(w, r, "workspaces_index.tmpl", data)
}
