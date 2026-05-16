package web

import (
	"html/template"
	"strings"
)

// SearchResultRow is one rendered match on the /search page.
// Snippet is the FTS engine's snippet with <<term>> highlight
// markers already converted to <mark> tags via MarkupSnippet so
// the template can render it as trusted HTML.
type SearchResultRow struct {
	Type    string // "spec" | "event"
	ID      string
	Title   string
	Snippet template.HTML
	Score   float64
	Href    string
}

// SearchOptions configure a Search call. Limit bounds the result
// count; zero falls back to the projection's default.
type SearchOptions struct {
	Limit int
}

// SearchProjection is the read-side surface the shared /search
// handler queries. Local resolvers wrap the SQLite FTS5 index in
// internal/core/search; central resolvers (when the Postgres FTS
// surface lands per central-node.DB.4) wrap that. v1 central
// shells leave this nil and the handler surfaces a
// "search backend not yet wired on central in v1" notice instead.
type SearchProjection interface {
	Search(query string, opts SearchOptions) ([]SearchResultRow, error)
}

// MarkupSnippet converts FTS5's `<<term>>` highlight markers into
// HTML <mark> tags. Source text is HTML-escaped first so a match
// that happens to contain HTML (e.g. an event payload that embeds
// HTML inside a JSON string) cannot inject markup. Both shells'
// projections route raw snippets through this helper so the
// rendered markup is identical (web-ui.SEARCH.2).
func MarkupSnippet(s string) template.HTML {
	escaped := template.HTMLEscapeString(s)
	escaped = strings.ReplaceAll(escaped, "&lt;&lt;", `<mark>`)
	escaped = strings.ReplaceAll(escaped, "&gt;&gt;", `</mark>`)
	return template.HTML(escaped)
}

// HrefForEntity maps an FTS result's (entityType, entityID) to
// its detail page URL on the local shell. Both shells share this
// helper so click-throughs are stable across UIs; the central
// shell's eventual workspace-scoped variant is a thin wrapper
// that prepends the org + workspace prefix.
//
// Unknown entity types resolve to "#" so the row is still
// clickable but the link is inert — matches the v1 fallback in
// the existing local shell.
func HrefForEntity(entityType, entityID string) string {
	switch entityType {
	case "spec":
		return "/specs/" + entityID
	case "event":
		// Event rows don't have a per-event detail page in v1;
		// /audit is the catch-all where the user can browse the
		// surrounding entries.
		return "/audit"
	}
	return "#"
}
