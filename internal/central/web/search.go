package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	internalweb "github.com/asabla/rex/internal/web"
)

// SearchHitReader is the central-side surface the web shell's
// search projection queries. The central server's PostgresSearch
// satisfies it directly; cmd/rex-central binds the concrete
// instance via NewGitStoreResolverWithSearch. v1 dev mode (no
// --db) leaves the projection nil and the handler renders the
// "not yet wired" notice unchanged.
type SearchHitReader interface {
	Search(ctx context.Context, workspaceID, query string, limit int) ([]SearchHit, error)
}

// SearchHit is the web-shell-side mirror of
// server.SearchHit. Kept independent so internal/central/web
// stays free of an internal/central/server import.
type SearchHit struct {
	EntityType string
	EntityID   string
	Title      string
	Snippet    string
	Score      float32
}

// centralSearchProjection satisfies internalweb.SearchProjection
// by dispatching against a workspace-scoped SearchHitReader
// (typically *server.PostgresSearch from cmd/rex-central).
type centralSearchProjection struct {
	backend     SearchHitReader
	workspaceID string
	ctx         context.Context
}

func newCentralSearchProjection(ctx context.Context, backend SearchHitReader, workspaceID string) centralSearchProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralSearchProjection{backend: backend, workspaceID: workspaceID, ctx: ctx}
}

func (p centralSearchProjection) Search(query string, opts internalweb.SearchOptions) ([]internalweb.SearchResultRow, error) {
	if p.backend == nil || p.workspaceID == "" {
		return nil, nil
	}
	hits, err := p.backend.Search(p.ctx, p.workspaceID, query, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("central search: %w", err)
	}
	rows := make([]internalweb.SearchResultRow, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, internalweb.SearchResultRow{
			Type:    h.EntityType,
			ID:      h.EntityID,
			Title:   h.Title,
			Snippet: internalweb.MarkupSnippet(h.Snippet),
			Score:   float64(h.Score),
			Href:    internalweb.HrefForEntity(h.EntityType, h.EntityID),
		})
	}
	return rows, nil
}

// centralSearchData backs search.tmpl on the central shell. The
// envelope mirrors the local one's field names so the shared
// template renders identically; central populates Notice with
// the v1 "backend not yet wired" hint because the Postgres FTS
// surface (central-node.DB.4) is not yet implemented.
//
// SaveSuccess / SaveError / Saved are local-only — central in v1
// has no per-user / per-workspace saved-search registry. They're
// declared here as zero-value placeholders so the shared template's
// {{if}} guards short-circuit rather than failing on a missing
// field at render time.
type centralSearchData struct {
	centralPageData
	Query       string
	Scope       string
	Limit       int
	Error       string
	Results     []internalweb.SearchResultRow
	Notice      string
	SaveSuccess string
	SaveError   string
	Saved       []savedPlaceholder
}

// savedPlaceholder is a zero-valued stand-in for the shared
// template's `.Saved` iteration. Replaced by a real saved-search
// row type if + when central grows a per-user saved-search store.
type savedPlaceholder struct{}

// handleSearch is GET /orgs/<org>/workspaces/<ws>/search. v1
// behaviour: render the page with the input form + the notice,
// and dispatch to ws.Search when present. The central shell
// today leaves Workspace.Search nil so the dispatch falls
// through to the empty-state view (no results). Postgres FTS
// hookup lands when central-node.DB.4 ships.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
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
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	scope := r.URL.Query().Get("scope")
	limit := 25
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, ok := parsePositiveInt(v); ok {
			limit = parsed
		}
	}
	data := centralSearchData{
		centralPageData: s.pageData(orgID, wsID, "search"),
		Query:           q,
		Scope:           scope,
		Limit:           limit,
	}
	if ws.Search == nil {
		data.Notice = "central search backend not yet wired in v1 — see central-node.DB.4 / central-read-side-search-amendments."
	} else if q != "" {
		rows, err := ws.Search.Search(q, internalweb.SearchOptions{Limit: limit})
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Results = rows
		}
	}
	s.renderer.Render(w, r, "search.tmpl", data)
}

// parsePositiveInt is the same small stdlib-free parser the local
// shell uses for the audit-limit query, duplicated here so the
// central package stays free of internal/local/web imports.
// Returns (n, true) for positive integers up to 9999.
func parsePositiveInt(s string) (int, bool) {
	if s == "" || len(s) > 4 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}
