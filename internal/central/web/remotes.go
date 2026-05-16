package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/asabla/rex/internal/local/remotes"
	internalweb "github.com/asabla/rex/internal/web"
)

// centralRemotesProjection satisfies
// internalweb.RemotesProjection by reading the workspace's
// `.rex/remotes.toml` entry from the GitStore (storage.WS.2.7).
// Read-only on central per the 2026-05-16 amendment Decision C —
// per-machine remote management stays on the local shell.
//
// Drafts / NeedsRebase / LastSeen stay at their zero values
// because those signals are per-machine: the central node has
// no equivalent watermark store. Templates render the resulting
// rows with em-dashes for the missing columns.
type centralRemotesProjection struct {
	store       GitEntityReader
	workspaceID string
	ctx         context.Context
}

func newCentralRemotesProjection(ctx context.Context, store GitEntityReader, workspaceID string) centralRemotesProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralRemotesProjection{store: store, workspaceID: workspaceID, ctx: ctx}
}

func (p centralRemotesProjection) ListRemotes() ([]internalweb.RemoteRow, error) {
	if p.store == nil || p.workspaceID == "" {
		return nil, nil
	}
	rec, err := p.store.Get(p.ctx, p.workspaceID, "remotes.toml")
	if err != nil {
		// Workspace without a synced remotes.toml — render an
		// empty list rather than 500.
		if errors.Is(err, errUnknownEntity) {
			return nil, nil
		}
		// Best-effort: treat any other Get error as "no entries"
		// (matches the spec projection's tolerance) so a fresh
		// central deployment with nothing pushed yet still
		// renders cleanly.
		return nil, nil
	}
	reg, err := remotes.ParseBytes([]byte(rec.Content))
	if err != nil {
		return nil, fmt.Errorf("central remotes: parse: %w", err)
	}
	rows := make([]internalweb.RemoteRow, 0, len(reg.List()))
	for _, r := range reg.List() {
		row := internalweb.RemoteRow{
			Name:        r.Name,
			URL:         r.URL,
			Fingerprint: r.Fingerprint,
		}
		if !r.AddedAt.IsZero() {
			row.AddedAt = r.AddedAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		if !r.LastSeen.IsZero() {
			row.LastSeen = r.LastSeen.UTC().Format("2006-01-02 15:04 UTC")
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// centralRemotesData backs remotes.tmpl on the central shell.
type centralRemotesData struct {
	centralPageData
	Rows   []internalweb.RemoteRow
	Source string
	// AddCmd intentionally left empty — central is read-only for
	// remotes, so the template's "add via …" hint is suppressed.
	AddCmd string
}

// handleRemotes is GET /orgs/<org>/workspaces/<ws>/remotes.
func (s *Server) handleRemotes(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	wsID := r.PathValue("ws")
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	if ws.Remotes == nil {
		http.Error(w, "central web: workspace has no remotes projection", http.StatusServiceUnavailable)
		return
	}
	rows, err := ws.Remotes.ListRemotes()
	if err != nil {
		http.Error(w, "central web: list remotes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := centralRemotesData{
		centralPageData: s.pageData(orgID, wsID, "remotes"),
		Rows:            rows,
		Source:          "the workspace's synced .rex/remotes.toml",
	}
	s.renderer.Render(w, r, "remotes.tmpl", data)
}
