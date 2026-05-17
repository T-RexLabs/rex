package web

import (
	"net/http"
	"strings"

	internalweb "github.com/asabla/rex/internal/web"
)

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
