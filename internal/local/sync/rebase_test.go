package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/central/server"
	"github.com/asabla/rex/internal/core/sync/conflict"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// pushSeed pushes a content blob into the central git store under
// the given entity path with no base revision (initial creation).
// Mirrors what `rex sync rebase` would do once shipped, just inlined
// here so the rebase tests can pre-state the server cleanly.
func pushSeed(t *testing.T, srv *server.Server, entity, content string) {
	t.Helper()
	rec := proto.GitEntity{
		Path:     entity,
		Revision: proto.GitContentRevision(content),
		Content:  content,
	}
	if err := srv.GitStore().Put(context.Background(), rec, ""); err != nil {
		t.Fatalf("seed git: %v", err)
	}
}

func writeLocal(t *testing.T, root, entity, content string) {
	t.Helper()
	path := filepath.Join(root, ".rex", entity)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readLocal(t *testing.T, root, entity string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".rex", entity))
	if err != nil {
		t.Fatalf("read %s: %v", entity, err)
	}
	return string(body)
}

// TestRebaseLocalOnlyWhenRemoteIsEmpty covers the first-rebase path:
// the central has never seen this entity, so the rebase reports
// LocalOnly and leaves the file untouched.
func TestRebaseLocalOnlyWhenRemoteIsEmpty(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	root := t.TempDir()
	writeLocal(t, root, "specs/x.yaml", "spec_version: 1\n")

	c := NewClient(hs.URL)
	res, err := c.RebaseEntity(context.Background(), RunArgs{
		WorkspaceRoot: root, Remote: "primary",
	}, "specs/x.yaml")
	if err != nil {
		t.Fatalf("RebaseEntity: %v", err)
	}
	if res.Outcome != RebaseLocalOnly {
		t.Fatalf("outcome: got %s want local-only", res.Outcome)
	}
	if got := readLocal(t, root, "specs/x.yaml"); got != "spec_version: 1\n" {
		t.Fatalf("file mutated unexpectedly: %q", got)
	}
}

// TestRebaseUnchangedRefreshesBaseCache: local and remote already
// agree on revision. The base cache gets stamped so a future rebase
// has the right ancestor.
func TestRebaseUnchangedRefreshesBaseCache(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	content := "name: alpha\n"
	pushSeed(t, srv, "workspace.yaml", content)

	root := t.TempDir()
	writeLocal(t, root, "workspace.yaml", content)

	c := NewClient(hs.URL)
	res, err := c.RebaseEntity(context.Background(), RunArgs{
		WorkspaceRoot: root, Remote: "primary",
	}, "workspace.yaml")
	if err != nil {
		t.Fatalf("RebaseEntity: %v", err)
	}
	if res.Outcome != RebaseUnchanged {
		t.Fatalf("outcome: got %s want unchanged", res.Outcome)
	}
	// Base cache should now exist.
	if _, err := os.Stat(gitBasePath(root, "primary", "workspace.yaml")); err != nil {
		t.Fatalf("base cache not written: %v", err)
	}
}

// TestRebaseCleanMergeWritesMerged: remote-only changes; local
// untouched. Rebase emits remote content into the local file.
func TestRebaseCleanMergeWritesMerged(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	base := "alpha\nbeta\n"
	remote := "alpha\nBETA\n"
	pushSeed(t, srv, "workspace.yaml", remote)

	root := t.TempDir()
	writeLocal(t, root, "workspace.yaml", base)
	// Pre-stamp base cache so the merge has a real ancestor.
	if err := saveBase(root, "primary", "workspace.yaml", []byte(base)); err != nil {
		t.Fatalf("saveBase: %v", err)
	}

	c := NewClient(hs.URL)
	res, err := c.RebaseEntity(context.Background(), RunArgs{
		WorkspaceRoot: root, Remote: "primary",
	}, "workspace.yaml")
	if err != nil {
		t.Fatalf("RebaseEntity: %v", err)
	}
	if res.Outcome != RebaseClean {
		t.Fatalf("outcome: got %s want clean", res.Outcome)
	}
	if got := readLocal(t, root, "workspace.yaml"); got != remote {
		t.Fatalf("merged file mismatch:\n%s\nwant:\n%s", got, remote)
	}
	// No sidecar should exist.
	side, err := conflict.Exists(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil || side {
		t.Fatalf("unexpected sidecar: %v exists=%v", err, side)
	}
}

// TestRebaseConflictWritesSidecar: both sides edit the same line.
// Local file gains conflict markers; sidecar is written; outcome is
// RebaseConflict.
func TestRebaseConflictWritesSidecar(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	base := "name: alpha\n"
	local := "name: local-edit\n"
	remote := "name: remote-edit\n"
	pushSeed(t, srv, "workspace.yaml", remote)

	root := t.TempDir()
	writeLocal(t, root, "workspace.yaml", local)
	if err := saveBase(root, "primary", "workspace.yaml", []byte(base)); err != nil {
		t.Fatalf("saveBase: %v", err)
	}

	c := NewClient(hs.URL)
	res, err := c.RebaseEntity(context.Background(), RunArgs{
		WorkspaceRoot: root, Remote: "primary",
	}, "workspace.yaml")
	if err != nil {
		t.Fatalf("RebaseEntity: %v", err)
	}
	if res.Outcome != RebaseConflict {
		t.Fatalf("outcome: got %s want conflict", res.Outcome)
	}
	if res.Hunks != 1 {
		t.Fatalf("hunks: got %d want 1", res.Hunks)
	}
	got := readLocal(t, root, "workspace.yaml")
	if !strings.Contains(got, "<<<<<<< local") {
		t.Fatalf("file missing conflict markers:\n%s", got)
	}
	side, err := conflict.Exists(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil || !side {
		t.Fatalf("expected sidecar; err=%v exists=%v", err, side)
	}
	sc, err := conflict.Read(filepath.Join(root, ".rex", "workspace.yaml.conflict"))
	if err != nil {
		t.Fatalf("Read sidecar: %v", err)
	}
	if sc.Remote != "primary" {
		t.Fatalf("sidecar remote: %q", sc.Remote)
	}
	if sc.LocalRevision == "" || sc.RemoteRevision == "" {
		t.Fatalf("sidecar missing revisions: %+v", sc)
	}
}

func TestRebaseRefusesNonGitMergedPath(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	root := t.TempDir()
	c := NewClient(hs.URL)
	for _, p := range []string{"events.log", "index.sqlite", "snapshots/x", "random.txt"} {
		_, err := c.RebaseEntity(context.Background(), RunArgs{
			WorkspaceRoot: root, Remote: "primary",
		}, p)
		if err == nil {
			t.Errorf("path %q: expected error, got nil", p)
		}
	}
}

// TestRebaseCleanClearsStaleSidecar: when a previous rebase left a
// sidecar but a fresh rebase resolves cleanly, the sidecar must go.
func TestRebaseCleanClearsStaleSidecar(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	content := "name: agreed\n"
	pushSeed(t, srv, "workspace.yaml", content)
	root := t.TempDir()
	writeLocal(t, root, "workspace.yaml", content)

	// Drop a stale sidecar from a previous failed rebase.
	stalePath := filepath.Join(root, ".rex", "workspace.yaml.conflict")
	if err := conflict.Write(stalePath, conflict.Sidecar{
		Entity: "workspace.yaml", Remote: "primary",
	}); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	c := NewClient(hs.URL)
	if _, err := c.RebaseEntity(context.Background(), RunArgs{
		WorkspaceRoot: root, Remote: "primary",
	}, "workspace.yaml"); err != nil {
		t.Fatalf("RebaseEntity: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale sidecar still present: err=%v", err)
	}
}
