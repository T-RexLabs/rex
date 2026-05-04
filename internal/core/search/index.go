package search

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// IndexFileName is the conventional filename under .rex/.
const IndexFileName = "index.sqlite"

// schema captures every CREATE statement the v1 indexer issues. Run
// in order at Open / Rebuild. Future schema bumps should land via a
// migration helper rather than editing these literals.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`,
	`INSERT OR IGNORE INTO schema_version(version) VALUES (1)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
		event_id UNINDEXED,
		timestamp_wall UNINDEXED,
		type,
		actor UNINDEXED,
		workspace_id UNINDEXED,
		payload
	)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS specs_fts USING fts5(
		spec_id UNINDEXED,
		path UNINDEXED,
		name,
		state UNINDEXED,
		description,
		raw_yaml
	)`,
}

// resetTables drops the FTS virtual tables ahead of a full rebuild.
// schema_version is preserved so a partial rebuild still leaves a
// valid version row in place.
var resetTables = []string{
	`DROP TABLE IF EXISTS events_fts`,
	`DROP TABLE IF EXISTS specs_fts`,
}

// Index is the local search index. A single Index value owns its
// underlying *sql.DB; methods are safe for concurrent use because
// SQL operations route through the database/sql connection pool
// and the Index uses an additional mutex for create/recreate races.
type Index struct {
	db *sql.DB
	mu sync.Mutex // guards Rebuild against concurrent UpsertEvent/UpsertSpec
}

// Open returns an Index backed by .rex/index.sqlite under the
// supplied workspace root. The file is created on first call. The
// directory ".rex/" must already exist (workspace init handles this).
func Open(workspaceRoot string) (*Index, error) {
	rexDir := filepath.Join(workspaceRoot, ".rex")
	if _, err := os.Stat(rexDir); err != nil {
		return nil, fmt.Errorf("search: .rex/ missing at %s: %w", workspaceRoot, err)
	}
	path := filepath.Join(rexDir, IndexFileName)
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("search: open %s: %w", path, err)
	}
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

// Close releases the underlying database handle.
func (idx *Index) Close() error {
	if idx == nil || idx.db == nil {
		return nil
	}
	return idx.db.Close()
}

func applySchema(db *sql.DB) error {
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("search: apply schema %q: %w", oneLine(stmt), err)
		}
	}
	return nil
}

func oneLine(s string) string {
	out := strings.ReplaceAll(s, "\n", " ")
	if idx := strings.Index(out, "  "); idx >= 0 {
		return out[:idx] + "..."
	}
	return out
}

// UpsertEvent indexes one eventlog.Record. Duplicate event IDs are
// replaced; the FTS table itself has no PK so we DELETE-then-INSERT
// to keep the row count bounded under repeated upserts.
func (idx *Index) UpsertEvent(rec eventlog.Record) error {
	if idx == nil || idx.db == nil {
		return errors.New("search: nil Index")
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("search: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM events_fts WHERE event_id = ?`, rec.ID); err != nil {
		return fmt.Errorf("search: delete event %q: %w", rec.ID, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO events_fts (event_id, timestamp_wall, type, actor, workspace_id, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Timestamp.Wall, rec.Type, rec.Actor, rec.WorkspaceID, string(rec.Payload),
	); err != nil {
		return fmt.Errorf("search: insert event %q: %w", rec.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("search: commit: %w", err)
	}
	return nil
}

// UpsertSpec indexes one parsed Document.
func (idx *Index) UpsertSpec(doc *specfmt.Document) error {
	if idx == nil || idx.db == nil {
		return errors.New("search: nil Index")
	}
	if doc.Metadata.ID == "" {
		return errors.New("search: cannot index spec with empty id")
	}
	rawYAML, err := readSpecBytes(doc.Path)
	if err != nil {
		return err
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("search: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM specs_fts WHERE spec_id = ?`, doc.Metadata.ID); err != nil {
		return fmt.Errorf("search: delete spec %q: %w", doc.Metadata.ID, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO specs_fts (spec_id, path, name, state, description, raw_yaml)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		doc.Metadata.ID, doc.Path, doc.Metadata.Name, doc.Metadata.State, doc.Description, rawYAML,
	); err != nil {
		return fmt.Errorf("search: insert spec %q: %w", doc.Metadata.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("search: commit: %w", err)
	}
	return nil
}

// readSpecBytes reads the raw YAML for a spec when the Document was
// loaded via ParseFile. Falls back to empty when Path is unset.
func readSpecBytes(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("search: read spec %s: %w", path, err)
	}
	return string(body), nil
}

// Rebuild drops the FTS tables and recreates them from the
// workspace's events.log + .rex/specs/ tree. Used by `rex workspace
// reindex` and as the init path when the index file is fresh.
func (idx *Index) Rebuild(workspaceRoot string) (Stats, error) {
	if idx == nil || idx.db == nil {
		return Stats{}, errors.New("search: nil Index")
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if _, err := idx.db.Exec(strings.Join(resetTables, ";")); err != nil {
		return Stats{}, fmt.Errorf("search: drop FTS: %w", err)
	}
	if err := applySchema(idx.db); err != nil {
		return Stats{}, err
	}

	stats := Stats{}
	if err := idx.rebuildEvents(workspaceRoot, &stats); err != nil {
		return stats, err
	}
	if err := idx.rebuildSpecs(workspaceRoot, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// Stats reports a Rebuild's outcome.
type Stats struct {
	Events int
	Specs  int
}

func (idx *Index) rebuildEvents(workspaceRoot string, stats *Stats) error {
	logPath := filepath.Join(workspaceRoot, ".rex", "events.log")
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	r, err := eventlog.OpenReader(logPath)
	if err != nil {
		return fmt.Errorf("search: open events.log: %w", err)
	}
	defer r.Close()

	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("search: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(
		`INSERT INTO events_fts (event_id, timestamp_wall, type, actor, workspace_id, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("search: prepare insert: %w", err)
	}
	defer stmt.Close()

	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("search: read events.log: %w", err)
		}
		if _, err := stmt.Exec(
			rec.ID, rec.Timestamp.Wall, rec.Type, rec.Actor, rec.WorkspaceID, string(rec.Payload),
		); err != nil {
			return fmt.Errorf("search: insert event %q: %w", rec.ID, err)
		}
		stats.Events++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("search: commit events: %w", err)
	}
	return nil
}

func (idx *Index) rebuildSpecs(workspaceRoot string, stats *Stats) error {
	specsDir := filepath.Join(workspaceRoot, ".rex", "specs")
	entries, err := os.ReadDir(specsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("search: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(
		`INSERT INTO specs_fts (spec_id, path, name, state, description, raw_yaml)
		 VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("search: prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(specsDir, e.Name())
		doc, err := specfmt.ParseFile(path)
		if err != nil {
			// Skip unparseable specs; the validator surfaces
			// them through its own surface.
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(
			doc.Metadata.ID, path, doc.Metadata.Name, doc.Metadata.State,
			doc.Description, string(body),
		); err != nil {
			return fmt.Errorf("search: insert spec %q: %w", doc.Metadata.ID, err)
		}
		stats.Specs++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("search: commit specs: %w", err)
	}
	return nil
}

// Result is one match. Snippet is the FTS5 snippet excerpt with
// match markers (<<>> by convention from sqlite snippet()).
type Result struct {
	EntityType  string `json:"entity_type"`  // "event" | "spec"
	WorkspaceID string `json:"workspace_id"`
	EntityID    string `json:"entity_id"`
	Title       string `json:"title"`
	Snippet     string `json:"snippet"`
	Score       float64 `json:"score"`
	URI         string `json:"uri"`
}

// SearchOptions configure a Search call.
type SearchOptions struct {
	// Limit caps the result count (default 25).
	Limit int
}

// Search runs the FTS query against both events_fts and specs_fts,
// merging and ordering results by score (lowest = best in FTS5).
// Workspace scope is implicit: each Index is rooted in one
// workspace's .rex/. Cross-workspace search lands when the global
// registry can enumerate locally-known workspaces.
func (idx *Index) Search(query string, opts SearchOptions) ([]Result, error) {
	if idx == nil || idx.db == nil {
		return nil, errors.New("search: nil Index")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("search: query is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 25
	}
	query = escapeFTSQuery(query)

	out := make([]Result, 0, limit)

	specRows, err := idx.db.Query(
		`SELECT spec_id, name, state,
			snippet(specs_fts, -1, '<<', '>>', '...', 12) AS sn,
			rank
		 FROM specs_fts
		 WHERE specs_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search: query specs: %w", err)
	}
	for specRows.Next() {
		var r Result
		var rank float64
		var name, state, snippet string
		if err := specRows.Scan(&r.EntityID, &name, &state, &snippet, &rank); err != nil {
			specRows.Close()
			return nil, err
		}
		r.EntityType = "spec"
		r.Title = name
		if state != "" {
			r.Title = fmt.Sprintf("%s (%s)", name, state)
		}
		r.Snippet = snippet
		r.Score = rank
		r.URI = fmt.Sprintf("rex://workspace/local/spec/%s", r.EntityID)
		out = append(out, r)
	}
	specRows.Close()

	eventRows, err := idx.db.Query(
		`SELECT event_id, type, workspace_id,
			snippet(events_fts, -1, '<<', '>>', '...', 12) AS sn,
			rank
		 FROM events_fts
		 WHERE events_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search: query events: %w", err)
	}
	for eventRows.Next() {
		var r Result
		var rank float64
		var typ, ws, snippet string
		if err := eventRows.Scan(&r.EntityID, &typ, &ws, &snippet, &rank); err != nil {
			eventRows.Close()
			return nil, err
		}
		r.EntityType = "event"
		r.WorkspaceID = ws
		r.Title = typ
		r.Snippet = snippet
		r.Score = rank
		r.URI = fmt.Sprintf("rex://workspace/%s/event/%s", ws, r.EntityID)
		out = append(out, r)
	}
	eventRows.Close()

	// Merge by score (lower is better in FTS5 rank).
	sortResults(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sortResults stable-sorts in place by score ascending.
func sortResults(rs []Result) {
	// stdlib sort.Slice would do but we keep this manual to avoid
	// the import for a 5-line helper.
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Score < rs[j-1].Score; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// IndexPath returns the canonical filename inside .rex/.
func IndexPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".rex", IndexFileName)
}

// escapeFTSQuery makes a free-form user query safe to pass to
// SQLite FTS5. FTS5 reads unquoted hyphens as NOT and unquoted
// colons as column qualifiers — both common in our event payload
// content (kebab-cased ids, "type:event"). This pre-pass tokenizes
// on whitespace and double-quotes any token containing FTS-special
// characters, while letting AND / OR / NOT operators pass through
// unquoted so users can still build compound queries.
func escapeFTSQuery(q string) string {
	tokens := strings.Fields(q)
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		upper := strings.ToUpper(t)
		if upper == "AND" || upper == "OR" || upper == "NOT" {
			out = append(out, upper)
			continue
		}
		if strings.ContainsAny(t, "-:.,;()'") || strings.HasPrefix(t, "\"") {
			t = strings.ReplaceAll(t, `"`, `""`)
			out = append(out, `"`+t+`"`)
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

// EventIndexer is the eventlog.OnAppend-shaped adapter the CLI
// composes alongside the hooks dispatcher. Failures are logged via
// the supplied onError (nil → silent) so a transient SQLite hiccup
// doesn't fail the writer.
func EventIndexer(idx *Index, onError func(error)) func(eventlog.Record) {
	return func(rec eventlog.Record) {
		if idx == nil {
			return
		}
		if err := idx.UpsertEvent(rec); err != nil && onError != nil {
			onError(err)
		}
	}
}
