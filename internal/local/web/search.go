package web

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/asabla/rex/internal/core/search"
)

// searchData backs search.tmpl.
type searchData struct {
	pageData
	Query   string
	Limit   int
	Error   string
	Results []searchResultRow
}

// searchResultRow is one rendered match. Snippet is FTS5's
// snippet excerpt with <<term>> highlight markers converted to
// <mark> tags by markupSnippet so the template can render it
// safely as HTML.
type searchResultRow struct {
	Type    string // "spec" | "event"
	ID      string
	Title   string
	Snippet template.HTML
	Score   float64
	Href    string // workspace-local URL the row links to
}

// handleSearch renders /search. The query comes in via ?q= so
// the URL is shareable / bookmarkable / browser-history-friendly
// (web-ui.SEARCH.3 future "saved searches" can land as a
// sidebar later; the same ?q= URL backs them).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 25
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, ok := parsePositiveInt(v); ok {
			limit = parsed
		}
	}
	base := s.basePageData()
	base.NavSection = "search"

	d := searchData{
		pageData: base,
		Query:    q,
		Limit:    limit,
	}
	if q == "" {
		s.render(w, r, "search.tmpl", d)
		return
	}

	idx, err := search.Open(s.opts.WorkspaceRoot)
	if err != nil {
		d.Error = "open index: " + err.Error()
		s.render(w, r, "search.tmpl", d)
		return
	}
	defer idx.Close()

	results, err := idx.Search(q, search.SearchOptions{Limit: limit})
	if err != nil {
		// search.Search returns a hard error on empty/garbled
		// queries; surface as a non-fatal banner.
		if errors.Is(err, errEmptyQuery) || strings.Contains(err.Error(), "query is required") {
			d.Error = "query is empty"
		} else {
			d.Error = err.Error()
		}
		s.render(w, r, "search.tmpl", d)
		return
	}
	for _, r := range results {
		d.Results = append(d.Results, searchResultRow{
			Type:    r.EntityType,
			ID:      r.EntityID,
			Title:   r.Title,
			Snippet: markupSnippet(r.Snippet),
			Score:   r.Score,
			Href:    hrefFor(r),
		})
	}
	s.render(w, r, "search.tmpl", d)
}

// errEmptyQuery is reserved for future use if we adopt a typed
// error from the search package.
var errEmptyQuery = errors.New("search: empty query")

// markupSnippet converts FTS5's `<<term>>` highlight markers into
// HTML <mark> tags. Source text is HTML-escaped first so a match
// that happens to contain HTML (e.g. an event payload that
// embeds HTML inside a JSON string) cannot inject markup.
func markupSnippet(s string) template.HTML {
	escaped := template.HTMLEscapeString(s)
	escaped = strings.ReplaceAll(escaped, "&lt;&lt;", `<mark>`)
	escaped = strings.ReplaceAll(escaped, "&gt;&gt;", `</mark>`)
	return template.HTML(escaped)
}

// hrefFor maps a search.Result to its detail page URL. Specs
// link to /specs/<id>; events link to /runs/<run-id> when we can
// pin a run id on the snippet, otherwise to /audit (the catch-all
// for non-runner events). v1 simplification: events whose
// EntityID looks like a run-event id link via /runs/<id> regardless
// of whether the id is the run id or the event id; the run-detail
// page handles unknown ids with a 404.
func hrefFor(r search.Result) string {
	switch r.EntityType {
	case "spec":
		return "/specs/" + r.EntityID
	case "event":
		// FTS event rows index every audit-class event including
		// run-lifecycle events. We don't have a /events/<id>
		// detail page (events live inside runs), so we point at
		// /audit for now. A follow-up could pin run id when the
		// row is a runner event.
		return "/audit"
	}
	return "#"
}
