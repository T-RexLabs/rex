package web

import (
	"context"
	"net/http"

	internalweb "github.com/asabla/rex/internal/web"
)

// GitWorkspacesLister is the opt-in subset of the central
// GitStore that exposes the set of workspace ids the store
// currently holds content for. MemoryGitStore implements it
// directly (no ctx needed); PostgresGitStore satisfies it via
// a SELECT DISTINCT against the git_entities table.
//
// Kept separate from GitEntityReader because per-entity reads
// are always workspace-scoped, while the index is the
// not-workspace-scoped pivot point that lets us enumerate them.
//
// Two shapes are accepted at the call site: a context-free
// listing (the in-memory store can satisfy that cheaply) and
// the context-aware listing required by Postgres. The web shell
// type-switches on whichever is implemented.
type GitWorkspacesLister interface {
	ListWorkspaces() []string
}

// GitWorkspacesListerCtx is the context-aware variant
// PostgresGitStore satisfies. Implementations that need
// transaction scope (RLS via app.current_org_id) take this
// interface; in-memory stores stick with the ctx-free
// GitWorkspacesLister. The orgID is supplied explicitly so
// the implementation can stamp app.current_org_id without the
// web layer needing to import internal/central/server.
type GitWorkspacesListerCtx interface {
	ListWorkspaces(ctx context.Context, orgID string) ([]string, error)
}

// centralWorkspacesIndexData backs workspaces_index.tmpl. OrgID
// rides through so the per-row links can build
// /orgs/<org-id>/workspaces/<ws-id>/specs URLs.
type centralWorkspacesIndexData struct {
	centralPageData
	Workspaces []internalweb.WorkspaceIndexRow
}

// handleWorkspacesIndex is GET /orgs/<org-id>/workspaces. The
// authoritative list of workspaces in an org lives in the
// `workspaces` binding table (populated first-push-wins via the
// events store's Append), so the handler asks Orgs first when
// it's bound. The GitStore enumeration is the fallback when no
// Orgs projection is wired (e.g. dev-mode in-memory) — it only
// catches workspaces with git-merged content, which misses
// events-only pushes.
func (s *Server) handleWorkspacesIndex(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
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
	cr, ok := s.opts.Resolver.(centralWorkspaceResolver)
	if !ok {
		http.Error(w, "central web: workspaces index requires a GitStore-backed resolver", http.StatusServiceUnavailable)
		return
	}
	var (
		ids []string
		err error
	)
	switch {
	case s.opts.Orgs != nil:
		ids, err = s.opts.Orgs.ListWorkspacesInOrg(orgID)
	case cr.git != nil:
		listerCtx, _ := cr.git.(GitWorkspacesListerCtx)
		lister, _ := cr.git.(GitWorkspacesLister)
		if listerCtx == nil && lister == nil {
			http.Error(w, "central web: GitStore does not implement GitWorkspacesLister", http.StatusServiceUnavailable)
			return
		}
		if listerCtx != nil {
			ids, err = listerCtx.ListWorkspaces(r.Context(), orgID)
		} else {
			ids = lister.ListWorkspaces()
		}
	default:
		http.Error(w, "central web: workspaces index requires an Orgs projection or a GitStore-backed resolver", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "central web: list workspaces: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]internalweb.WorkspaceIndexRow, 0, len(ids))
	for _, id := range ids {
		row := internalweb.WorkspaceIndexRow{ID: id}
		// Best-effort metadata read from the git store. Misses
		// gracefully when content hasn't synced yet.
		if cr.git != nil {
			if rec, gerr := cr.git.Get(r.Context(), id, "workspace.yaml"); gerr == nil {
				fields := parseWorkspaceFields(rec.Content)
				row.Name = fields.Name
				row.State = fields.State
			}
		}
		rows = append(rows, row)
	}
	data := centralWorkspacesIndexData{
		centralPageData: centralPageData{
			Workspace:   nil,
			BindAddr:    s.opts.BindAddr,
			Version:     s.opts.Version,
			NavSection:  "workspaces",
			OrgID:       orgID,
			WorkspaceID: "",
			CentralOnly: true,
			Shell:       "central",
		},
		Workspaces: rows,
	}
	s.renderer.Render(w, r, "workspaces_index.tmpl", data)
}
