package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/local/savedsearch"
	internalweb "github.com/asabla/rex/internal/web"
)

// searchResultRow aliases the shared row type so existing template
// references stay valid.
type searchResultRow = internalweb.SearchResultRow

// searchData backs search.tmpl.
type searchData struct {
	pageData
	Query   string
	Scope   string
	Limit   int
	Error   string
	Results []searchResultRow
	// Notice is a one-shot informational banner shown above the
	// results (e.g. on central: "search backend not yet wired in
	// v1"). Empty on the local shell.
	Notice string
	// Saved is the merged set of per-user + per-workspace saved
	// searches (web-ui.SEARCH.3). Workspace entries shadow user
	// entries on name collision; the Source field hints at scope.
	Saved []savedsearch.SavedSearchView
	// SaveError surfaces a per-form error from "save current
	// search" without losing the typed-in query.
	SaveError string
	// SaveSuccess is the saved-name on a successful save so the
	// page can show a one-shot confirmation.
	SaveSuccess string
}

// localSearchProjection satisfies internalweb.SearchProjection
// against the local SQLite FTS5 index.
type localSearchProjection struct{ root string }

func (l localSearchProjection) Search(query string, opts internalweb.SearchOptions) ([]searchResultRow, error) {
	idx, err := search.Open(l.root)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	defer idx.Close()
	results, err := idx.Search(query, search.SearchOptions{Limit: opts.Limit})
	if err != nil {
		return nil, err
	}
	rows := make([]searchResultRow, 0, len(results))
	for _, res := range results {
		rows = append(rows, searchResultRow{
			Type:    res.EntityType,
			ID:      res.EntityID,
			Title:   res.Title,
			Snippet: internalweb.MarkupSnippet(res.Snippet),
			Score:   res.Score,
			Href:    internalweb.HrefForEntity(res.EntityType, res.EntityID),
		})
	}
	return rows, nil
}

// handleSearch renders /search. The query comes in via ?q= so the
// URL is shareable / bookmarkable / browser-history-friendly. The
// saved-searches sidebar (web-ui.SEARCH.3) reads from
// internal/local/savedsearch's per-workspace + per-user registry —
// clicking a saved entry simply loads the same /search?q=... URL.
//
// Scope (web-ui.SEARCH.1): the ?scope= param echoes the topbar
// selector. v1 only the empty / "current workspace" scope actually
// dispatches to a search backend; "remote:<name>" / "*" are accepted
// and round-tripped to the form so the URL is shareable, but the
// cross-remote dispatch lands with the search-engine cluster.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	scope := r.URL.Query().Get("scope")
	limit := 25
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, ok := parsePositiveInt(v); ok {
			limit = parsed
		}
	}
	base := s.basePageData()
	base.NavSection = "search"
	base.SearchScope.Selected = scope

	d := searchData{
		pageData: base,
		Query:    q,
		Scope:    scope,
		Limit:    limit,
		Saved:    loadMergedSavedSearches(s.opts.WorkspaceRoot),
	}

	switch r.Method {
	case http.MethodGet:
		s.runSearch(w, r, &d)
	case http.MethodPost:
		s.handleSearchSave(w, r, &d)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// runSearch executes a query when one is present, then renders the
// page. Empty queries fall through to the empty-state view.
func (s *Server) runSearch(w http.ResponseWriter, r *http.Request, d *searchData) {
	if d.Query == "" {
		s.render(w, r, "search.tmpl", *d)
		return
	}
	rows, err := localSearchProjection{root: s.opts.WorkspaceRoot}.Search(d.Query, internalweb.SearchOptions{Limit: d.Limit})
	if err != nil {
		if errors.Is(err, errEmptyQuery) || strings.Contains(err.Error(), "query is required") {
			d.Error = "query is empty"
		} else {
			d.Error = err.Error()
		}
		s.render(w, r, "search.tmpl", *d)
		return
	}
	d.Results = rows
	s.render(w, r, "search.tmpl", *d)
}

// handleSearchSave persists a "save current search" submission
// (web-ui.SEARCH.3). The form posts (name, query) and we Set into
// the per-workspace registry so the saved entry travels with the
// workspace via sync. After a successful save the user lands back
// on the same /search?q=... page with a one-shot confirmation.
func (s *Server) handleSearchSave(w http.ResponseWriter, r *http.Request, d *searchData) {
	if err := r.ParseForm(); err != nil {
		d.SaveError = "decode form: " + err.Error()
		s.runSearch(w, r, d)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	query := strings.TrimSpace(r.FormValue("query"))
	d.Query = query
	if name == "" || query == "" {
		d.SaveError = "name and query are both required"
		s.runSearch(w, r, d)
		return
	}
	if !savedsearch.IsValidName(name) {
		d.SaveError = "name must be kebab-case (a-z, 0-9, hyphens)"
		s.runSearch(w, r, d)
		return
	}
	regPath := savedsearch.WorkspacePath(s.opts.WorkspaceRoot)
	reg, err := savedsearch.Load(regPath)
	if err != nil {
		d.SaveError = "load saved-searches: " + err.Error()
		s.runSearch(w, r, d)
		return
	}
	if err := reg.Set(savedsearch.SavedSearch{Name: name, Query: query}); err != nil {
		d.SaveError = err.Error()
		s.runSearch(w, r, d)
		return
	}
	if err := savedsearch.Save(regPath, reg); err != nil {
		d.SaveError = "save: " + err.Error()
		s.runSearch(w, r, d)
		return
	}
	// Reload merged list so the new entry appears in the sidebar
	// without a second round-trip.
	d.Saved = loadMergedSavedSearches(s.opts.WorkspaceRoot)
	d.SaveSuccess = name
	s.runSearch(w, r, d)
}

// loadMergedSavedSearches returns the per-workspace + per-user merged
// saved-search list, best-effort. Errors yield an empty slice so the
// search page still renders.
func loadMergedSavedSearches(workspaceRoot string) []savedsearch.SavedSearchView {
	wsReg, err := savedsearch.Load(savedsearch.WorkspacePath(workspaceRoot))
	if err != nil {
		wsReg = nil
	}
	var userReg *savedsearch.Registry
	if userPath, err := savedsearch.UserPath(); err == nil {
		userReg, _ = savedsearch.Load(userPath)
	}
	return savedsearch.MergedView(wsReg, userReg)
}

// _ keeps fmt referenced in case future error formatters land here.
var _ = fmt.Errorf

// errEmptyQuery is reserved for future use if we adopt a typed
// error from the search package.
var errEmptyQuery = errors.New("search: empty query")
