package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SearchHit is one rendered match the central search surface
// returns. Mirrors internal/core/search.Result's wire shape
// without coupling to the SQLite-backed type — both shells'
// projections map onto the same web-side SearchResultRow.
type SearchHit struct {
	EntityType string // "spec" | "event"
	EntityID   string
	Title      string
	Snippet    string  // <<term>> markers for the shared web markup
	Score      float32 // ts_rank; higher is better (opposite of FTS5)
}

// PostgresSearch is the Postgres-backed FTS surface added in
// schema step 7. Runs the @@ query against both events.tsv and
// git_entities.tsv, merges by ts_rank, and snippets via
// ts_headline. Scoped to one (org, workspace) per call so
// per-workspace pages don't leak content across tenants.
type PostgresSearch struct {
	parent *PostgresStore
}

// NewPostgresSearch returns a Search surface backed by the same
// pool the events store uses. The pool's migrations must include
// step 7 (events.tsv + git_entities.tsv generated columns); the
// parent's migrate() runs every step on startup so this
// constructor is safe right after server.New.
func NewPostgresSearch(parent *PostgresStore) *PostgresSearch {
	return &PostgresSearch{parent: parent}
}

// Search runs a workspace-scoped FTS query and returns up to
// limit results. Empty workspaceID + empty query both return
// errors so the caller doesn't silently scan everything.
//
// The query string is interpreted as a websearch_to_tsquery —
// Postgres's user-input-friendly variant that handles quoted
// phrases, AND/OR, and bare terms without requiring the caller
// to escape lexer punctuation. Same UX shape as the local
// shell's escapeFTSQuery helper, just delegated to Postgres.
func (s *PostgresSearch) Search(ctx context.Context, workspaceID, query string, limit int) ([]SearchHit, error) {
	if workspaceID == "" {
		return nil, errors.New("server: search requires a non-empty workspace_id")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("server: search query is required")
	}
	if limit <= 0 {
		limit = 25
	}
	var out []SearchHit
	err := s.parent.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)

		// Specs: only the canonical specs/<id>.yaml entries —
		// amendments live under specs/_proposed/... and surface
		// on the /amendments page instead.
		specRows, err := tx.Query(ctx, `
			SELECT path, content,
			       ts_headline('english', content, websearch_to_tsquery('english', $3),
			           'StartSel=<<, StopSel=>>, MaxFragments=1, MaxWords=20, MinWords=5') AS snippet,
			       ts_rank(tsv, websearch_to_tsquery('english', $3)) AS rank
			FROM   git_entities
			WHERE  org_id = $1 AND workspace_id = $2
			AND    path LIKE 'specs/%.yaml'
			AND    path NOT LIKE 'specs/_proposed/%'
			AND    tsv @@ websearch_to_tsquery('english', $3)
			ORDER  BY rank DESC
			LIMIT  $4
		`, orgID, workspaceID, query, limit)
		if err != nil {
			return fmt.Errorf("server: search specs: %w", err)
		}
		for specRows.Next() {
			var path, content, snippet string
			var rank float32
			if err := specRows.Scan(&path, &content, &snippet, &rank); err != nil {
				specRows.Close()
				return fmt.Errorf("server: search specs scan: %w", err)
			}
			out = append(out, SearchHit{
				EntityType: "spec",
				EntityID:   specIDFromPath(path),
				Title:      specIDFromPath(path),
				Snippet:    snippet,
				Score:      rank,
			})
		}
		specRows.Close()
		if err := specRows.Err(); err != nil {
			return fmt.Errorf("server: search specs iter: %w", err)
		}

		// Events: index over payload::text. The title surfaces
		// the event type so the rendered hit list is scannable;
		// the snippet shows the matching JSON fragment.
		eventRows, err := tx.Query(ctx, `
			SELECT id, type,
			       ts_headline('english', payload::text, websearch_to_tsquery('english', $3),
			           'StartSel=<<, StopSel=>>, MaxFragments=1, MaxWords=20, MinWords=5') AS snippet,
			       ts_rank(tsv, websearch_to_tsquery('english', $3)) AS rank
			FROM   events
			WHERE  org_id = $1 AND workspace_id = $2
			AND    tsv @@ websearch_to_tsquery('english', $3)
			ORDER  BY rank DESC
			LIMIT  $4
		`, orgID, workspaceID, query, limit)
		if err != nil {
			return fmt.Errorf("server: search events: %w", err)
		}
		for eventRows.Next() {
			var id, typ, snippet string
			var rank float32
			if err := eventRows.Scan(&id, &typ, &snippet, &rank); err != nil {
				eventRows.Close()
				return fmt.Errorf("server: search events scan: %w", err)
			}
			out = append(out, SearchHit{
				EntityType: "event",
				EntityID:   id,
				Title:      typ,
				Snippet:    snippet,
				Score:      rank,
			})
		}
		eventRows.Close()
		return eventRows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Merge by score, highest first (ts_rank: higher = better).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// specIDFromPath extracts the spec id from a GitStore path of
// the form "specs/<id>.yaml". Returns the path unchanged when
// it doesn't match; the search caller's filter ensures only
// matching paths reach here.
func specIDFromPath(path string) string {
	const prefix, suffix = "specs/", ".yaml"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return path
	}
	return strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
}
