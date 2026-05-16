package server

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// freshPostgresGitStore builds a PostgresStore + a paired
// PostgresGitStore against a per-test schema. Mirrors
// freshPostgresStore's setup; the git store rides on the events
// store's pool.
func freshPostgresGitStore(t *testing.T) (*PostgresStore, *PostgresGitStore, context.Context) {
	t.Helper()
	store, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, store)
	return store, NewPostgresGitStore(store), ctx
}

// TestPostgresGitStorePutGetRoundtrip covers the happy path:
// initial Put with empty base, Get returns what was stored
// (path, revision, content, signature, actor).
func TestPostgresGitStorePutGetRoundtrip(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)

	rec := proto.GitEntity{
		Path:      "specs/sync.yaml",
		Revision:  proto.GitContentRevision("spec_version: 1\n"),
		Content:   "spec_version: 1\n",
		Signature: "deadbeef",
		Actor:     "l-test",
	}
	if err := gs.Put(ctx, "ws-1", rec, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := gs.Get(ctx, "ws-1", "specs/sync.yaml")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Path != rec.Path {
		t.Errorf("Path: got %q want %q", got.Path, rec.Path)
	}
	if got.Revision != rec.Revision {
		t.Errorf("Revision: got %q want %q", got.Revision, rec.Revision)
	}
	if got.Content != rec.Content {
		t.Errorf("Content: got %q want %q", got.Content, rec.Content)
	}
	if got.Signature != rec.Signature {
		t.Errorf("Signature: got %q want %q", got.Signature, rec.Signature)
	}
	if got.Actor != rec.Actor {
		t.Errorf("Actor: got %q want %q", got.Actor, rec.Actor)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt: zero (want server-stamped time)")
	}
}

// TestPostgresGitStoreGetUnknownIsErrUnknownGitEntity confirms
// the not-found path matches MemoryGitStore's contract.
func TestPostgresGitStoreGetUnknownIsErrUnknownGitEntity(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)
	_, err := gs.Get(ctx, "ws-1", "specs/never-pushed.yaml")
	if err == nil {
		t.Fatal("Get: expected error for missing entity")
	}
	if !errors.Is(err, ErrUnknownGitEntity) {
		t.Errorf("err: got %v want ErrUnknownGitEntity", err)
	}
}

// TestPostgresGitStoreScopesByWorkspace mirrors the in-memory
// store's isolation test: two workspaces with the same path
// don't leak content into each other.
func TestPostgresGitStoreScopesByWorkspace(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)

	a := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-a", Content: "alpha"}
	b := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-b", Content: "beta"}
	if err := gs.Put(ctx, "ws-a", a, ""); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := gs.Put(ctx, "ws-b", b, ""); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	got, err := gs.Get(ctx, "ws-a", "workspace.yaml")
	if err != nil {
		t.Fatalf("Get a: %v", err)
	}
	if got.Content != "alpha" {
		t.Errorf("ws-a content: got %q want alpha", got.Content)
	}
	got, err = gs.Get(ctx, "ws-b", "workspace.yaml")
	if err != nil {
		t.Fatalf("Get b: %v", err)
	}
	if got.Content != "beta" {
		t.Errorf("ws-b content: got %q want beta", got.Content)
	}

	pathsA, _ := gs.List(ctx, "ws-a")
	pathsB, _ := gs.List(ctx, "ws-b")
	if len(pathsA) != 1 || pathsA[0] != "workspace.yaml" {
		t.Errorf("ws-a list: %v", pathsA)
	}
	if len(pathsB) != 1 || pathsB[0] != "workspace.yaml" {
		t.Errorf("ws-b list: %v", pathsB)
	}

	// Unknown workspace yields not-found, never the entity from
	// some other workspace.
	if _, err := gs.Get(ctx, "ws-ghost", "workspace.yaml"); !errors.Is(err, ErrUnknownGitEntity) {
		t.Errorf("ws-ghost get: got %v want ErrUnknownGitEntity", err)
	}

	// ListWorkspaces enumerates both.
	ids, err := gs.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ids) != 2 || ids[0] != "ws-a" || ids[1] != "ws-b" {
		t.Errorf("ListWorkspaces: %v want [ws-a ws-b]", ids)
	}
}

// TestPostgresGitStorePutConflictReturnsCurrent covers the
// base-revision mismatch branch: a stale baseRev gets a
// *GitRevisionConflictError carrying the server's current
// revision so the rebase engine can three-way merge.
func TestPostgresGitStorePutConflictReturnsCurrent(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)

	first := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-1", Content: "alpha"}
	if err := gs.Put(ctx, "ws-1", first, ""); err != nil {
		t.Fatalf("first Put: %v", err)
	}

	// Stale base — pretend we never saw rev-1.
	second := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-2", Content: "beta"}
	err := gs.Put(ctx, "ws-1", second, "stale-rev")
	if err == nil {
		t.Fatal("Put: expected conflict")
	}
	var conflict *GitRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if conflict.ServerCurrent.Revision != "rev-1" {
		t.Errorf("ServerCurrent.Revision: got %q want rev-1", conflict.ServerCurrent.Revision)
	}
	if conflict.ServerCurrent.Content != "alpha" {
		t.Errorf("ServerCurrent.Content: got %q want alpha", conflict.ServerCurrent.Content)
	}
}

// TestPostgresGitStorePutEmptyBaseAfterCreatedConflicts: a Put
// with empty baseRev against an existing entity is the "client
// thinks it's a brand-new file" race — must surface as conflict,
// not silently overwrite.
func TestPostgresGitStorePutEmptyBaseAfterCreatedConflicts(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)

	first := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-1", Content: "alpha"}
	if err := gs.Put(ctx, "ws-1", first, ""); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-2", Content: "beta"}
	err := gs.Put(ctx, "ws-1", second, "")
	if err == nil {
		t.Fatal("Put: expected conflict on empty-base re-create")
	}
	var conflict *GitRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if conflict.ServerCurrent.Revision != "rev-1" {
		t.Errorf("ServerCurrent.Revision: got %q want rev-1", conflict.ServerCurrent.Revision)
	}
}

// TestPostgresGitStorePutUpdatesOnMatchingBase covers the happy
// rebase-then-push path: the client pushes a new revision with
// the server's current as the base; the server accepts and
// overwrites.
func TestPostgresGitStorePutUpdatesOnMatchingBase(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)

	first := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-1", Content: "alpha"}
	if err := gs.Put(ctx, "ws-1", first, ""); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-2", Content: "beta"}
	if err := gs.Put(ctx, "ws-1", second, "rev-1"); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	got, err := gs.Get(ctx, "ws-1", "workspace.yaml")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Revision != "rev-2" || got.Content != "beta" {
		t.Errorf("update not persisted: got %+v", got)
	}
}

// TestPostgresGitStoreRejectsEmptyWorkspaceID confirms the
// input-validation branch matches the in-memory store.
func TestPostgresGitStoreRejectsEmptyWorkspaceID(t *testing.T) {
	t.Parallel()
	_, gs, ctx := freshPostgresGitStore(t)
	rec := proto.GitEntity{Path: "x", Revision: "r", Content: "c"}
	if err := gs.Put(ctx, "", rec, ""); err == nil {
		t.Error("Put(\"\"): expected error")
	}
	if _, err := gs.Get(ctx, "", "x"); err == nil {
		t.Error("Get(\"\"): expected error")
	}
	if _, err := gs.List(ctx, ""); err == nil {
		t.Error("List(\"\"): expected error")
	}
}
