package savedsearch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddListRemoveRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "saved-searches.toml")

	reg, err := Load(path)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatal("fresh registry should be empty")
	}

	if err := reg.Add(SavedSearch{Name: "recent-runs", Query: "type:run.completed"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := reg.Add(SavedSearch{Name: "auth-flow", Query: "auth"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Save(path, reg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "[searches.recent-runs]") {
		t.Fatalf("missing header: %s", body)
	}
	if !strings.Contains(string(body), `query = "type:run.completed"`) {
		t.Fatalf("missing query: %s", body)
	}

	// Round-trip via Load.
	reg2, err := Load(path)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	got := reg2.List()
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Name != "auth-flow" || got[1].Name != "recent-runs" {
		t.Fatalf("not lex-sorted: %+v", got)
	}

	if err := reg2.Remove("auth-flow"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := Save(path, reg2); err != nil {
		t.Fatalf("Save after remove: %v", err)
	}
	reg3, _ := Load(path)
	if len(reg3.List()) != 1 {
		t.Fatalf("post-remove: %v", reg3.List())
	}
}

func TestAddRejectsDuplicate(t *testing.T) {
	t.Parallel()
	reg := &Registry{Searches: map[string]SavedSearch{}}
	if err := reg.Add(SavedSearch{Name: "x", Query: "q"}); err != nil {
		t.Fatal(err)
	}
	err := reg.Add(SavedSearch{Name: "x", Query: "q2"})
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestSetUpserts(t *testing.T) {
	t.Parallel()
	reg := &Registry{Searches: map[string]SavedSearch{}}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := reg.Set(SavedSearch{Name: "x", Query: "q1", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	// Update preserves CreatedAt when Set is called without one.
	if err := reg.Set(SavedSearch{Name: "x", Query: "q2"}); err != nil {
		t.Fatal(err)
	}
	got, _ := reg.Get("x")
	if got.Query != "q2" || !got.CreatedAt.Equal(created) {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestRejectsBadName(t *testing.T) {
	t.Parallel()
	reg := &Registry{Searches: map[string]SavedSearch{}}
	for _, bad := range []string{"", "Bad", "with space", "trailing-", "-leading"} {
		if err := reg.Add(SavedSearch{Name: bad, Query: "q"}); err == nil {
			t.Fatalf("expected error for name %q", bad)
		}
	}
}

func TestRejectsEmptyQuery(t *testing.T) {
	t.Parallel()
	reg := &Registry{Searches: map[string]SavedSearch{}}
	if err := reg.Add(SavedSearch{Name: "x", Query: "  "}); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Parallel()
	reg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("expected empty: %v", reg.List())
	}
}

func TestMergedViewWorkspaceShadowsUser(t *testing.T) {
	t.Parallel()
	user := &Registry{Searches: map[string]SavedSearch{
		"shared":    {Name: "shared", Query: "user-version"},
		"user-only": {Name: "user-only", Query: "u"},
	}}
	ws := &Registry{Searches: map[string]SavedSearch{
		"shared":  {Name: "shared", Query: "ws-version"},
		"ws-only": {Name: "ws-only", Query: "w"},
	}}
	view := MergedView(ws, user)
	if len(view) != 3 {
		t.Fatalf("count: %d", len(view))
	}
	byName := map[string]SavedSearchView{}
	for _, v := range view {
		byName[v.Name] = v
	}
	if byName["shared"].Query != "ws-version" || byName["shared"].Source != SourceWorkspace {
		t.Fatalf("workspace should shadow user: %+v", byName["shared"])
	}
	if byName["user-only"].Source != SourceUser {
		t.Fatalf("user-only should be sourced from user: %+v", byName["user-only"])
	}
	if byName["ws-only"].Source != SourceWorkspace {
		t.Fatalf("ws-only should be sourced from workspace: %+v", byName["ws-only"])
	}
}

func TestMergedViewHandlesNilRegistries(t *testing.T) {
	t.Parallel()
	if got := MergedView(nil, nil); len(got) != 0 {
		t.Fatalf("two nil should yield empty, got %v", got)
	}
	user := &Registry{Searches: map[string]SavedSearch{"x": {Name: "x", Query: "q"}}}
	got := MergedView(nil, user)
	if len(got) != 1 || got[0].Source != SourceUser {
		t.Fatalf("nil workspace + populated user: %v", got)
	}
}
