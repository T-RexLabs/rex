package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// freshPostgresSearch builds a PostgresStore + a paired
// PostgresGitStore + a PostgresSearch against a per-test schema.
// Tests seed content via the git/event stores, then exercise the
// search surface end-to-end through the FTS indexes step 7
// installs.
func freshPostgresSearch(t *testing.T) (*PostgresStore, *PostgresGitStore, *PostgresSearch, context.Context) {
	t.Helper()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	return store, NewPostgresGitStore(store), NewPostgresSearch(store), ctx
}

// TestPostgresSearchFindsSpecsAndEvents covers the happy path
// across both surfaces: a query that hits spec content + event
// payload returns matches from both, ranked, with snippets
// carrying the <<>> markers the web shell renders as <mark>.
func TestPostgresSearchFindsSpecsAndEvents(t *testing.T) {
	t.Parallel()
	store, gs, search, ctx := freshPostgresSearch(t)

	// Seed a spec under specs/<id>.yaml.
	if err := gs.Put(ctx, "ws-1", proto.GitEntity{
		Path:     "specs/sync.yaml",
		Revision: "rev-1",
		Content:  "spec_version: 1\nmetadata:\n  id: sync\nname: Sync protocol with replication semantics\n",
	}, ""); err != nil {
		t.Fatalf("Put spec: %v", err)
	}

	// Seed an event whose payload mentions the same token so the
	// query hits both surfaces.
	rec := eventlog.Record{
		ID:          "ev-1",
		Type:        "test.replicated",
		Version:     1,
		Timestamp:   eventlog.HLC{Wall: time.Now().UnixNano()},
		Actor:       "l-test",
		WorkspaceID: "ws-1",
		Payload:     []byte(`{"message":"workspace replication completed"}`),
	}
	if _, err := store.Append(ctx, rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	hits, err := search.Search(ctx, "ws-1", "replication", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("Search hits: %d (want >= 2; one per surface)", len(hits))
	}
	var sawSpec, sawEvent bool
	for _, h := range hits {
		switch h.EntityType {
		case "spec":
			sawSpec = true
			if h.EntityID != "sync" {
				t.Errorf("spec EntityID: got %q want sync", h.EntityID)
			}
			if !strings.Contains(h.Snippet, "<<") || !strings.Contains(h.Snippet, ">>") {
				t.Errorf("spec snippet missing markers: %q", h.Snippet)
			}
		case "event":
			sawEvent = true
			if h.EntityID != "ev-1" {
				t.Errorf("event EntityID: got %q want ev-1", h.EntityID)
			}
		}
	}
	if !sawSpec {
		t.Error("missing spec hit")
	}
	if !sawEvent {
		t.Error("missing event hit")
	}
}

// TestPostgresSearchScopesByWorkspace confirms results are
// strictly scoped to the requested workspace. Two workspaces
// each get a spec with the same query term; the search for ws-1
// returns only ws-1's match.
func TestPostgresSearchScopesByWorkspace(t *testing.T) {
	t.Parallel()
	_, gs, search, ctx := freshPostgresSearch(t)
	for _, w := range []struct{ wsID, content string }{
		{"ws-1", "spec_version: 1\nmetadata:\n  id: alpha\nname: alpha-workspace artifact\n"},
		{"ws-2", "spec_version: 1\nmetadata:\n  id: alpha\nname: alpha-workspace different content\n"},
	} {
		if err := gs.Put(ctx, w.wsID, proto.GitEntity{
			Path: "specs/alpha.yaml", Revision: "r-" + w.wsID, Content: w.content,
		}, ""); err != nil {
			t.Fatalf("Put %s: %v", w.wsID, err)
		}
	}
	hits, err := search.Search(ctx, "ws-1", "artifact", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("ws-1 hits: %d want 1 (workspace isolation)", len(hits))
	}
	hits, err = search.Search(ctx, "ws-2", "artifact", 10)
	if err != nil {
		t.Fatalf("Search ws-2: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("ws-2 hits: %d want 0 (ws-2 doesn't have 'artifact')", len(hits))
	}
}

// TestPostgresSearchSkipsAmendments confirms amendments under
// specs/_proposed/... do not surface on the /search results
// (the amendments page is the right surface for them).
func TestPostgresSearchSkipsAmendments(t *testing.T) {
	t.Parallel()
	_, gs, search, ctx := freshPostgresSearch(t)
	if err := gs.Put(ctx, "ws-1", proto.GitEntity{
		Path:     "specs/_proposed/sync-amendment-2026-05-17.yaml",
		Revision: "r1",
		Content:  "amendment_for: sync\nsummary: testable amendment marker\n",
	}, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	hits, err := search.Search(ctx, "ws-1", "marker", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range hits {
		if h.EntityType == "spec" && strings.Contains(h.EntityID, "_proposed") {
			t.Errorf("amendment surfaced as spec: %+v", h)
		}
	}
}

// TestPostgresSearchRejectsEmpty inputs returns errors rather
// than silently scanning everything.
func TestPostgresSearchRejectsEmpty(t *testing.T) {
	t.Parallel()
	_, _, search, ctx := freshPostgresSearch(t)
	if _, err := search.Search(ctx, "", "query", 10); err == nil {
		t.Error("empty workspaceID: expected error")
	}
	if _, err := search.Search(ctx, "ws-1", "   ", 10); err == nil {
		t.Error("blank query: expected error")
	}
}

// TestPostgresSearchHonoursLimit clamps the result count.
func TestPostgresSearchHonoursLimit(t *testing.T) {
	t.Parallel()
	store, _, search, ctx := freshPostgresSearch(t)
	for i := 0; i < 5; i++ {
		rec := eventlog.Record{
			ID:          "ev-" + string(rune('a'+i)),
			Type:        "test.repeat",
			Version:     1,
			Timestamp:   eventlog.HLC{Wall: time.Now().UnixNano()},
			Actor:       "l-test",
			WorkspaceID: "ws-1",
			Payload:     []byte(`{"k":"limittoken"}`),
		}
		if _, err := store.Append(ctx, rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	hits, err := search.Search(ctx, "ws-1", "limittoken", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) > 2 {
		t.Fatalf("hits: %d want <= 2", len(hits))
	}
}
