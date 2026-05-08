package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpsertAddsThenReplaces(t *testing.T) {
	t.Parallel()

	r := &Registry{}
	r.Upsert(Entry{ID: "a", Path: "/abs/a"})
	if len(r.Entries) != 1 {
		t.Fatalf("after first add: %v", r.Entries)
	}
	if r.Entries[0].LastSeen.IsZero() {
		t.Fatal("LastSeen should be auto-stamped")
	}

	// Same (id, remote) → replace.
	r.Upsert(Entry{ID: "a", Path: "/abs/a-moved"})
	if len(r.Entries) != 1 || r.Entries[0].Path != "/abs/a-moved" {
		t.Fatalf("upsert should replace, got %v", r.Entries)
	}

	// Same id, different remote → append.
	r.Upsert(Entry{ID: "a", Remote: "primary", Path: "/abs/a-other"})
	if len(r.Entries) != 2 {
		t.Fatalf("different remote should be a new row, got %v", r.Entries)
	}
}

func TestRemoveByIDRemote(t *testing.T) {
	t.Parallel()

	r := &Registry{}
	r.Upsert(Entry{ID: "a", Path: "/abs/a"})
	r.Upsert(Entry{ID: "a", Remote: "primary", Path: "/abs/a-other"})

	if !r.Remove("a", "primary") {
		t.Fatal("remove of existing entry should report true")
	}
	if len(r.Entries) != 1 || r.Entries[0].Remote != "" {
		t.Fatalf("wrong entry survived: %v", r.Entries)
	}
	if r.Remove("ghost", "") {
		t.Fatal("remove of non-existent should report false")
	}
}

func TestRemoveByPath(t *testing.T) {
	t.Parallel()

	r := &Registry{}
	r.Upsert(Entry{ID: "a", Path: "/abs/a"})
	r.Upsert(Entry{ID: "b", Path: "/abs/a"}) // duplicate path, different id
	r.Upsert(Entry{ID: "c", Path: "/abs/c"})

	n := r.RemoveByPath("/abs/a")
	if n != 2 {
		t.Fatalf("RemoveByPath: got %d, want 2", n)
	}
	if len(r.Entries) != 1 || r.Entries[0].ID != "c" {
		t.Fatalf("survivors: %v", r.Entries)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "registry.toml")

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	r := &Registry{Entries: []Entry{
		{ID: "a", Path: "/abs/a", LastSeen: now},
		{ID: "a", Remote: "primary", Path: "/abs/a-prim", LastSeen: now},
		{ID: "b", Path: "/abs/b", LastSeen: now},
	}}
	if err := Save(path, r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "[[workspaces]]") {
		t.Fatalf("expected [[workspaces]] in TOML output:\n%s", body)
	}

	r2, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r2.Entries) != 3 {
		t.Fatalf("count: %d", len(r2.Entries))
	}
	// Sorted by (id, remote): a/"" < a/primary < b/""
	if r2.Entries[0].ID != "a" || r2.Entries[0].Remote != "" {
		t.Fatalf("sort: %+v", r2.Entries[0])
	}
	if r2.Entries[1].ID != "a" || r2.Entries[1].Remote != "primary" {
		t.Fatalf("sort: %+v", r2.Entries[1])
	}
	if r2.Entries[2].ID != "b" {
		t.Fatalf("sort: %+v", r2.Entries[2])
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Parallel()
	r, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(r.Entries) != 0 {
		t.Fatalf("expected empty: %v", r.Entries)
	}
}

func TestEntryValidate(t *testing.T) {
	t.Parallel()
	if err := (Entry{ID: "", Path: "/abs/a"}).Validate(); err == nil {
		t.Fatal("empty id should fail")
	}
	if err := (Entry{ID: "with space", Path: "/abs/a"}).Validate(); err == nil {
		t.Fatal("whitespace in id should fail")
	}
	if err := (Entry{ID: "a", Path: "rel/path"}).Validate(); err == nil {
		t.Fatal("relative path should fail")
	}
	if err := (Entry{ID: "a", Path: "/abs/a"}).Validate(); err != nil {
		t.Fatalf("valid entry: %v", err)
	}
}

func TestFindByIDRemote(t *testing.T) {
	t.Parallel()
	r := &Registry{Entries: []Entry{
		{ID: "a", Path: "/abs/a"},
		{ID: "a", Remote: "primary", Path: "/abs/a-prim"},
	}}
	if got := r.Find("a", ""); got == nil || got.Path != "/abs/a" {
		t.Fatalf("local-only lookup: %+v", got)
	}
	if got := r.Find("a", "primary"); got == nil || got.Path != "/abs/a-prim" {
		t.Fatalf("remote-scoped lookup: %+v", got)
	}
	if got := r.Find("ghost", ""); got != nil {
		t.Fatalf("non-existent should be nil: %+v", got)
	}
}
