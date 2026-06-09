//go:build central_e2e

package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPushOnlyPersistsRebaseFlagOnConflict covers sync.DRAFT.2: when
// a push returns 409, the per-remote watermark gets stamped with
// NeedsRebase=true plus the server head reported on the conflict, so
// `rex status` (and any other read-only consumer) can flag the
// remote without re-issuing a network call.
func TestPushOnlyPersistsRebaseFlagOnConflict(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	// Server has an event the client doesn't know about — pushing
	// from since="" will diverge.
	_, _ = srv.Store().Append(context.Background(), mkRec("server-head-1"))

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")
	f, _ := openAppend(logPath)
	if err := appendRaw(f, mkRec("local-draft")); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	_ = f.Close()

	c := NewClient(hs.URL)
	_, err := c.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	})
	if !IsConflict(err) {
		t.Fatalf("err: got %v want *ConflictError", err)
	}

	wm, err := LoadWatermark(wsRoot, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if !wm.NeedsRebase {
		t.Fatal("watermark.NeedsRebase: got false want true")
	}
	if wm.LastConflictHead != "server-head-1" {
		t.Fatalf("LastConflictHead: got %q want server-head-1", wm.LastConflictHead)
	}
	// Conflict path must NOT silently advance the ack pointer.
	if wm.LastAckedEventID != "" {
		t.Fatalf("LastAckedEventID: got %q want empty", wm.LastAckedEventID)
	}
}

// TestPushOnlySuccessClearsRebaseFlag covers the recovery path: once
// the user has rebased and a fresh push succeeds, the rebase-needed
// flag clears.
func TestPushOnlySuccessClearsRebaseFlag(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")
	f, _ := openAppend(logPath)
	if err := appendRaw(f, mkRec("a")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = f.Close()

	// Pre-stamp a stale rebase-needed flag — what the watermark
	// would look like after a previous conflict.
	if err := SaveWatermark(wsRoot, Watermark{
		Remote: "primary", NeedsRebase: true, LastConflictHead: "abc",
	}); err != nil {
		t.Fatalf("SaveWatermark: %v", err)
	}

	c := NewClient(hs.URL)
	if _, err := c.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	}); err != nil {
		t.Fatalf("PushOnly: %v", err)
	}

	wm, err := LoadWatermark(wsRoot, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if wm.NeedsRebase {
		t.Fatal("NeedsRebase: still true after successful push")
	}
	if wm.LastConflictHead != "" {
		t.Fatalf("LastConflictHead: got %q want empty", wm.LastConflictHead)
	}
}

// TestPullOnlyClearsRebaseFlag covers the rebase recovery path: a
// successful pull (the standard "fetch the diverging tail" step)
// clears the rebase-needed flag because we now have everything the
// remote did at request time.
func TestPullOnlyClearsRebaseFlag(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	_, _ = srv.Store().Append(context.Background(), mkRec("server-1"))

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")

	if err := SaveWatermark(wsRoot, Watermark{
		Remote: "primary", NeedsRebase: true, LastConflictHead: "server-1",
	}); err != nil {
		t.Fatalf("SaveWatermark: %v", err)
	}

	c := NewClient(hs.URL)
	if _, err := c.PullOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	}); err != nil {
		t.Fatalf("PullOnly: %v", err)
	}

	wm, err := LoadWatermark(wsRoot, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if wm.NeedsRebase {
		t.Fatal("NeedsRebase: still true after successful pull")
	}
	if wm.LastAckedEventID != "server-1" {
		t.Fatalf("LastAckedEventID: got %q", wm.LastAckedEventID)
	}
}

// TestRebaseFlagSurvivesProcessRestart confirms sync.DRAFT.2's
// durable signal: a process running `rex push` and hitting a
// conflict, then exiting, leaves the flag on disk for the next
// `rex status` call to find.
func TestRebaseFlagSurvivesProcessRestart(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	_, _ = srv.Store().Append(context.Background(), mkRec("server-only"))

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")
	f, _ := openAppend(logPath)
	_ = appendRaw(f, mkRec("local-draft"))
	_ = f.Close()

	c1 := NewClient(hs.URL)
	if _, err := c1.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	}); !IsConflict(err) {
		t.Fatalf("first PushOnly: got %v want *ConflictError", err)
	}

	// Simulate a fresh process: build a brand-new client and load
	// the watermark from disk.
	c2 := NewClient(hs.URL)
	_ = c2 // silence unused
	wm, err := LoadWatermark(wsRoot, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if !wm.NeedsRebase {
		t.Fatal("flag did not survive across the boundary")
	}
}
