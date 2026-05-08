package primindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/search"
)

// initRexDir creates the minimal .rex/ skeleton primindex needs to
// open the index against. specs/ is empty (rebuild handles missing
// content gracefully) and events.log is absent (also fine).
func initRexDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{"", "specs"} {
		if err := os.MkdirAll(filepath.Join(root, ".rex", sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	return root
}

func TestIndexingRebuildsEmptyWorkspace(t *testing.T) {
	t.Parallel()

	root := initRexDir(t)
	prim := New(Options{WorkspaceRoot: root})
	out, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "indexing", Type: PrimitiveType},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got Output
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Events != 0 || got.Specs != 0 {
		t.Fatalf("expected zero counts on empty workspace: %+v", got)
	}
}

func TestIndexingRequiresWorkspaceRoot(t *testing.T) {
	t.Parallel()

	prim := New(Options{})
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "indexing", Type: PrimitiveType},
	})
	if err == nil {
		t.Fatal("expected error when WorkspaceRoot is empty")
	}
	if !strings.Contains(err.Error(), "WorkspaceRoot") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestIndexingPicksUpSeededSpec(t *testing.T) {
	t.Parallel()

	root := initRexDir(t)
	specPath := filepath.Join(root, ".rex", "specs", "demo.yaml")
	body := `spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
description: indexing test fixture
`
	if err := os.WriteFile(specPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	prim := New(Options{WorkspaceRoot: root})
	out, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "indexing", Type: PrimitiveType},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got Output
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Specs != 1 {
		t.Fatalf("expected 1 spec indexed, got %d", got.Specs)
	}
}

func TestIndexingHonoursPreOpenedIndex(t *testing.T) {
	t.Parallel()

	root := initRexDir(t)
	idx, err := search.Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	prim := New(Options{WorkspaceRoot: root, Indexer: idx})
	if _, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "indexing", Type: PrimitiveType},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Index handle is reusable after the primitive returns.
	if _, err := idx.Search("anything", search.SearchOptions{}); err != nil && !strings.Contains(err.Error(), "query is required") {
		// "query is required" is fine — it just proves the handle is alive.
		t.Fatalf("idx.Search: %v", err)
	}
}

func TestIndexingDecodeError(t *testing.T) {
	t.Parallel()
	root := initRexDir(t)
	prim := New(Options{WorkspaceRoot: root})
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "indexing", Type: PrimitiveType, Config: []byte("not json")},
	})
	if err == nil || !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("expected decode error, got %v", err)
	}
}
