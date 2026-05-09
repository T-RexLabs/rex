package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// initWorkspace builds a minimal TempDir workspace shape so the
// indexer has something to walk. Pre-populates events.log via the
// eventlog Writer + a couple of YAML specs under .rex/specs/.
func initWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	rex := filepath.Join(root, ".rex")
	if err := os.MkdirAll(filepath.Join(rex, "specs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

// seedEvents writes N events via eventlog.Writer so events.log
// looks like the real thing.
func seedEvents(t *testing.T, root string, recs []eventlog.Record) {
	t.Helper()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(root, ".rex", "events.log"),
		WorkspaceID: "demo",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()
	for _, rec := range recs {
		if _, err := w.Append(rec.Type, rec.Version, rec.Payload); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func seedSpec(t *testing.T, root, id, body string) {
	t.Helper()
	path := filepath.Join(root, ".rex", "specs", id+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

func TestOpenAndCloseIndex(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// File exists.
	if _, err := os.Stat(IndexPath(root)); err != nil {
		t.Fatalf("index file missing: %v", err)
	}
}

func TestUpsertAndSearchEvent(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer idx.Close()

	rec := eventlog.Record{
		ID:          "ev-1",
		Type:        "workspace.created",
		Version:     1,
		Actor:       "l-aaaaaaaaaaaaaaaa",
		WorkspaceID: "demo",
		Payload:     json.RawMessage(`{"name":"Demo Workspace","note":"smoke test"}`),
	}
	if err := idx.UpsertEvent(rec); err != nil {
		t.Fatalf("UpsertEvent: %v", err)
	}

	results, err := idx.Search("smoke", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len: got %d (%+v)", len(results), results)
	}
	if results[0].EntityID != "ev-1" {
		t.Fatalf("entity_id: got %q", results[0].EntityID)
	}
	if !strings.Contains(results[0].Snippet, "smoke") {
		t.Fatalf("snippet missing match: %q", results[0].Snippet)
	}
}

func TestUpsertEventReplaceSemantics(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	rec := eventlog.Record{
		ID: "dup-1", Type: "test", Version: 1,
		Actor: "l-x", WorkspaceID: "ws", Payload: json.RawMessage(`{"k":"v1"}`),
	}
	for i := 0; i < 5; i++ {
		if err := idx.UpsertEvent(rec); err != nil {
			t.Fatalf("UpsertEvent %d: %v", i, err)
		}
	}
	results, _ := idx.Search("v1", SearchOptions{})
	if len(results) != 1 {
		t.Fatalf("upsert should be 1-row: got %d", len(results))
	}
}

func TestUpsertAndSearchSpec(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	seedSpec(t, root, "alpha", `spec_version: 1
metadata:
  id: alpha
  name: Alpha spec
  state: draft
description: |
  This spec covers the alpha API surface.
components:
  AUTH:
    name: Authentication
    requirements:
      "1": Validate every request signed with ed25519
`)
	doc, err := specfmt.ParseFile(filepath.Join(root, ".rex", "specs", "alpha.yaml"))
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	if err := idx.UpsertSpec(doc); err != nil {
		t.Fatalf("UpsertSpec: %v", err)
	}

	results, err := idx.Search("ed25519", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.EntityType == "spec" && r.EntityID == "alpha" {
			found = true
			if !strings.Contains(r.Snippet, "ed25519") {
				t.Fatalf("snippet missing match: %q", r.Snippet)
			}
		}
	}
	if !found {
		t.Fatalf("expected alpha spec in results: %+v", results)
	}
}

func TestRebuildFromWorkspace(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	seedSpec(t, root, "rebuild-target", `spec_version: 1
metadata: {id: rebuild-target, name: Rebuild target, state: draft}
description: |
  searchable-token-XYZ
`)
	seedEvents(t, root, []eventlog.Record{
		{Type: "workspace.created", Version: 1, Payload: json.RawMessage(`{"note":"rebuild-event-token"}`)},
	})

	idx, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Rebuild(root)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Specs != 1 {
		t.Fatalf("specs indexed: got %d want 1", stats.Specs)
	}
	if stats.Events != 1 {
		t.Fatalf("events indexed: got %d want 1", stats.Events)
	}

	results, err := idx.Search("searchable-token-XYZ", SearchOptions{})
	if err != nil {
		t.Fatalf("Search spec: %v", err)
	}
	if len(results) != 1 || results[0].EntityType != "spec" {
		t.Fatalf("expected one spec result: %+v", results)
	}

	results, err = idx.Search("rebuild-event-token", SearchOptions{})
	if err != nil {
		t.Fatalf("Search event: %v", err)
	}
	if len(results) != 1 || results[0].EntityType != "event" {
		t.Fatalf("expected one event result: %+v", results)
	}
}

func TestSearchReturnsResultsOrderedByScore(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	// Two events; one mentions "rare-keyword" once, the other
	// mentions it three times. FTS5's BM25 should rank the
	// denser one higher (lower rank value).
	mk := func(id string, payload string) eventlog.Record {
		return eventlog.Record{
			ID: id, Type: "x", Version: 1, Actor: "a", WorkspaceID: "ws",
			Payload: json.RawMessage(payload),
		}
	}
	_ = idx.UpsertEvent(mk("ev-sparse", `{"text":"rare-keyword once."}`))
	_ = idx.UpsertEvent(mk("ev-dense", `{"text":"rare-keyword rare-keyword rare-keyword denser."}`))

	results, err := idx.Search("rare-keyword", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len: %d", len(results))
	}
	// Lower score = better match.
	if results[0].Score >= results[1].Score {
		t.Fatalf("expected dense match first: %+v", results)
	}
	if results[0].EntityID != "ev-dense" {
		t.Fatalf("dense match should rank first: %+v", results)
	}
}

func TestSearchEmptyQueryErrors(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	if _, err := idx.Search("   ", SearchOptions{}); err == nil {
		t.Fatal("empty query should error")
	}
}

func TestSearchEventsFiltersByType(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	mk := func(id, typ, payload string) eventlog.Record {
		return eventlog.Record{
			ID: id, Type: typ, Version: 1, Actor: "a", WorkspaceID: "ws",
			Payload: json.RawMessage(payload),
		}
	}
	_ = idx.UpsertEvent(mk("e1", "run.completed", `{"text":"common-word in run"}`))
	_ = idx.UpsertEvent(mk("e2", "node.started", `{"text":"common-word in node"}`))
	_ = idx.UpsertEvent(mk("e3", "schedule.added", `{"text":"common-word in schedule"}`))

	// No filter — all three match.
	results, err := idx.SearchEvents("common-word", SearchEventsOptions{})
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Single-type filter narrows to one.
	results, err = idx.SearchEvents("common-word", SearchEventsOptions{
		Types: []string{"run.completed"},
	})
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(results) != 1 || results[0].EventID != "e1" {
		t.Fatalf("type filter shape: %+v", results)
	}

	// Multi-type filter accepts a list.
	results, err = idx.SearchEvents("common-word", SearchEventsOptions{
		Types: []string{"run.completed", "schedule.added"},
	})
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("multi-type filter: %+v", results)
	}
}

func TestEscapeFTSQueryColumnQualifier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		columns map[string]struct{}
		want    string
	}{
		{"events-type-qualifier", "type:run.completed", eventsFTSColumns, `type:"run.completed"`},
		{"events-type-qualifier-2", "type:repo.added", eventsFTSColumns, `type:"repo.added"`},
		{"specs-name-qualifier", "name:Foo", specsFTSColumns, `name:"Foo"`},
		{"specs-description-qualifier", "description:bar", specsFTSColumns, `description:"bar"`},
		// Same query against specs_fts → qualifier doesn't apply
		// (specs_fts has no type column); falls back to literal-
		// phrase form so the SQL stays valid.
		{"type-on-specs-fallback", "type:foo", specsFTSColumns, `"type:foo"`},
		// Unknown / UNINDEXED column keys fall back to the
		// conservative full-token quoting path.
		{"unindexed-actor", "actor:l-abc", eventsFTSColumns, `"actor:l-abc"`},
		{"unindexed-event-id", "event_id:42", eventsFTSColumns, `"event_id:42"`},
		{"url-prefix", "https://x.io", eventsFTSColumns, `"https://x.io"`},
		// Bare tokens stay untouched.
		{"bare", "hello", eventsFTSColumns, "hello"},
		// Operators pass through.
		{"and", "AND", eventsFTSColumns, "AND"},
		{"or", "OR", eventsFTSColumns, "OR"},
		// nil columns disables qualifier rewriting entirely.
		{"nil-columns", "type:run.completed", nil, `"type:run.completed"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := escapeFTSQuery(tc.input, tc.columns); got != tc.want {
				t.Errorf("escapeFTSQuery(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSearchEventsColumnQualifierNarrows(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	mk := func(id, typ, payload string) eventlog.Record {
		return eventlog.Record{
			ID: id, Type: typ, Version: 1, Actor: "a", WorkspaceID: "ws",
			Payload: json.RawMessage(payload),
		}
	}
	_ = idx.UpsertEvent(mk("e1", "run.completed", `{"text":"common-word"}`))
	_ = idx.UpsertEvent(mk("e2", "node.started", `{"text":"common-word"}`))

	// Without the new column qualifier, "type:run.completed" used
	// to be auto-quoted into a literal phrase that didn't match
	// either record. Now it acts as an FTS5 column qualifier and
	// narrows to e1.
	results, err := idx.SearchEvents("type:run.completed", SearchEventsOptions{})
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(results) != 1 || results[0].EventID != "e1" {
		t.Fatalf("type-qualifier should narrow to e1; got %+v", results)
	}
}

func TestSearchEventsEmptyQueryErrors(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	if _, err := idx.SearchEvents("  ", SearchEventsOptions{}); err == nil {
		t.Fatal("empty query should error")
	}
}

func TestSearchLimit(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	for i := 0; i < 30; i++ {
		_ = idx.UpsertEvent(eventlog.Record{
			ID: "ev-" + repeat("a", i+1), Type: "t", Version: 1,
			Actor: "a", WorkspaceID: "ws",
			Payload: json.RawMessage(`{"text":"common-word"}`),
		})
	}
	results, _ := idx.Search("common-word", SearchOptions{Limit: 5})
	if len(results) != 5 {
		t.Fatalf("limit not respected: got %d", len(results))
	}
}

func TestEventIndexerNilSafe(t *testing.T) {
	t.Parallel()

	fn := EventIndexer(nil, nil)
	// Calling with nil index must not panic.
	fn(eventlog.Record{ID: "x"})
}

func TestEventIndexerIndexesViaCallback(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	idx, _ := Open(root)
	defer idx.Close()

	fn := EventIndexer(idx, nil)
	fn(eventlog.Record{
		ID: "ev-callback", Type: "t", Version: 1,
		Actor: "a", WorkspaceID: "ws",
		Payload: json.RawMessage(`{"text":"callback-token"}`),
	})

	results, _ := idx.Search("callback-token", SearchOptions{})
	if len(results) != 1 || results[0].EntityID != "ev-callback" {
		t.Fatalf("indexer callback failed: %+v", results)
	}
}

func TestRebuildIsIdempotent(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	seedEvents(t, root, []eventlog.Record{
		{Type: "x", Version: 1, Payload: json.RawMessage(`{"k":"once-only"}`)},
	})
	idx, _ := Open(root)
	defer idx.Close()

	for i := 0; i < 3; i++ {
		if _, err := idx.Rebuild(root); err != nil {
			t.Fatalf("Rebuild %d: %v", i, err)
		}
	}
	results, _ := idx.Search("once-only", SearchOptions{})
	if len(results) != 1 {
		t.Fatalf("repeated Rebuilds should not duplicate rows: got %d", len(results))
	}
}

// repeat is a tiny helper; avoiding strings.Repeat for the test
// avoids importing it just for this.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
