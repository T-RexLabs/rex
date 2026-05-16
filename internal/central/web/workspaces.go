package web

import (
	"context"
	"net/http"

	internalweb "github.com/asabla/rex/internal/web"
)

// centralWorkspacesIndexProjection satisfies
// internalweb.WorkspacesIndexProjection by reading the one
// workspace bound to the central GitStore. v1 single-workspace
// limitation: the orgID is ignored — the projection returns the
// single workspace.yaml the store holds. When the multi-workspace
// GitStore refactor lands the orgID gets dispatched against the
// workspaces table.
type centralWorkspacesIndexProjection struct {
	store GitEntityReader
	ctx   context.Context
}

func newCentralWorkspacesIndexProjection(ctx context.Context, store GitEntityReader) centralWorkspacesIndexProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralWorkspacesIndexProjection{store: store, ctx: ctx}
}

func (p centralWorkspacesIndexProjection) ListWorkspaces(orgID string) ([]internalweb.WorkspaceIndexRow, error) {
	if p.store == nil {
		return nil, nil
	}
	rec, err := p.store.Get(p.ctx, "workspace.yaml")
	if err != nil {
		// No workspace.yaml synced yet — render empty rather
		// than 500.
		return nil, nil
	}
	fields := parseWorkspaceFields(rec.Content)
	if fields.ID == "" {
		return nil, nil
	}
	return []internalweb.WorkspaceIndexRow{{
		ID:    fields.ID,
		Name:  fields.Name,
		State: fields.State,
	}}, nil
}

// centralWorkspacesIndexData backs workspaces_index.tmpl. OrgID
// rides through so the per-row links can build
// /orgs/<org-id>/workspaces/<ws-id>/specs URLs.
type centralWorkspacesIndexData struct {
	centralPageData
	Workspaces []internalweb.WorkspaceIndexRow
}

// handleWorkspacesIndex is GET /orgs/<org-id>/workspaces.
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
	// Workspaces index resolves at the org level — there's no
	// ws-id in the URL. We reach for the GitStore directly via
	// the WorkspaceResolver's concrete type when it's the
	// central one, which keeps the wireup focused on the v1
	// single-workspace GitStore.
	cr, ok := s.opts.Resolver.(centralWorkspaceResolver)
	if !ok || cr.git == nil {
		http.Error(w, "central web: workspaces index requires a GitStore-backed resolver", http.StatusServiceUnavailable)
		return
	}
	rows, err := newCentralWorkspacesIndexProjection(context.Background(), cr.git).ListWorkspaces(orgID)
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
