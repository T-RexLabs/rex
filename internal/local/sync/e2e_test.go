//go:build central_e2e

package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/rex-centralized/central/server"
)

// TestTwoLocalsViaCentralRoundTrip wires up one in-process central
// and two distinct local workspaces. Local A pushes its
// workspace.created; Local B pulls; B's events.log is asserted to
// contain A's record. This is the smallest end-to-end exercise of
// the sync wire across actor boundaries.
//
// Note: the test deliberately does NOT call Sync() on B. With the
// push-first ordering and B's own workspace.created already in its
// log, Sync would attempt a push that conflicts (B has never seen
// the server's events; A's record is the server's HEAD). The
// conflict path is correct and intentional — bidirectional
// round-trip without an explicit rebase requires sync.GIT.* (the
// rebase engine), which is not yet implemented.
func TestTwoLocalsViaCentralRoundTrip(t *testing.T) {
	t.Parallel()

	srv, err := server.New(server.Options{})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Local A: workspace + one record.
	rootA := makeWorkspace(t, "ws-a")
	logA := filepath.Join(rootA, ".rex", "events.log")
	recA := mkRecWithActor("a-event-1", "l-aaaaaaaaaaaaaaaa", "ws-a")
	writeRaw(t, logA, recA)

	// Local B: workspace + one record (so its log is non-empty,
	// matching the realistic state after `rex workspace init`).
	rootB := makeWorkspace(t, "ws-b")
	logB := filepath.Join(rootB, ".rex", "events.log")
	recB := mkRecWithActor("b-event-1", "l-bbbbbbbbbbbbbbbb", "ws-b")
	writeRaw(t, logB, recB)

	// A pushes — server now has [a-event-1].
	clientA := NewClient(hs.URL)
	pushRes, err := clientA.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: rootA, Remote: "primary", EventsLogPath: logA,
	})
	if err != nil {
		t.Fatalf("A push: %v", err)
	}
	if pushRes.Accepted != 1 || pushRes.HeadID != "a-event-1" {
		t.Fatalf("A push result: %+v", pushRes)
	}

	// B pulls — should receive A's record.
	clientB := NewClient(hs.URL)
	pulled, err := clientB.PullOnly(context.Background(), RunArgs{
		WorkspaceRoot: rootB, Remote: "primary", EventsLogPath: logB,
	})
	if err != nil {
		t.Fatalf("B pull: %v", err)
	}
	if pulled != 1 {
		t.Fatalf("B pulled: got %d want 1", pulled)
	}

	// B's events.log now has [b-event-1, a-event-1]. The watermark
	// for "primary" advances to a-event-1.
	gotIDs := readAllIDs(t, logB)
	if len(gotIDs) != 2 || gotIDs[0] != "b-event-1" || gotIDs[1] != "a-event-1" {
		t.Fatalf("B log: got %v want [b-event-1 a-event-1]", gotIDs)
	}

	wm, err := LoadWatermark(rootB, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if wm.LastAckedEventID != "a-event-1" {
		t.Fatalf("B watermark: got %q want a-event-1", wm.LastAckedEventID)
	}

	// A run further on its side should not affect B until B pulls
	// again. Push a second record from A.
	recA2 := mkRecWithActor("a-event-2", "l-aaaaaaaaaaaaaaaa", "ws-a")
	writeRaw(t, logA, recA2)
	pushRes2, err := clientA.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: rootA, Remote: "primary", EventsLogPath: logA,
	})
	if err != nil {
		t.Fatalf("A push 2: %v", err)
	}
	if pushRes2.Accepted != 1 || pushRes2.HeadID != "a-event-2" {
		t.Fatalf("A push 2 result: %+v", pushRes2)
	}

	// B's log shouldn't have changed yet — B has not pulled.
	gotMid := readAllIDs(t, logB)
	if len(gotMid) != 2 {
		t.Fatalf("B log changed without pulling: %v", gotMid)
	}

	// B pulls again.
	pulled2, err := clientB.PullOnly(context.Background(), RunArgs{
		WorkspaceRoot: rootB, Remote: "primary", EventsLogPath: logB,
	})
	if err != nil {
		t.Fatalf("B pull 2: %v", err)
	}
	if pulled2 != 1 {
		t.Fatalf("B pulled 2: got %d want 1", pulled2)
	}
	gotFinal := readAllIDs(t, logB)
	wantFinal := []string{"b-event-1", "a-event-1", "a-event-2"}
	if len(gotFinal) != len(wantFinal) {
		t.Fatalf("B final log: got %v want %v", gotFinal, wantFinal)
	}
	for i := range wantFinal {
		if gotFinal[i] != wantFinal[i] {
			t.Fatalf("B final log @ %d: got %q want %q", i, gotFinal[i], wantFinal[i])
		}
	}
}

// TestTwoLocalsViaCentralAfterRebaseStub asserts the divergence
// path: when both A and B push first, only A succeeds; B sees a
// ConflictError. This documents the v1 limitation and the role
// rebase will play.
func TestTwoLocalsViaCentralBothPushDiverges(t *testing.T) {
	t.Parallel()

	srv, _ := server.New(server.Options{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	rootA := makeWorkspace(t, "ws-a")
	logA := filepath.Join(rootA, ".rex", "events.log")
	writeRaw(t, logA, mkRecWithActor("a-1", "l-aaaaaaaaaaaaaaaa", "ws-a"))

	rootB := makeWorkspace(t, "ws-b")
	logB := filepath.Join(rootB, ".rex", "events.log")
	writeRaw(t, logB, mkRecWithActor("b-1", "l-bbbbbbbbbbbbbbbb", "ws-b"))

	clientA := NewClient(hs.URL)
	if _, err := clientA.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: rootA, Remote: "primary", EventsLogPath: logA,
	}); err != nil {
		t.Fatalf("A push: %v", err)
	}

	clientB := NewClient(hs.URL)
	_, err := clientB.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: rootB, Remote: "primary", EventsLogPath: logB,
	})
	if !IsConflict(err) {
		t.Fatalf("B push: got %v want *ConflictError", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("As: %v", err)
	}
	if ce.ServerHead != "a-1" {
		t.Fatalf("server head: got %q want a-1", ce.ServerHead)
	}
	if len(ce.DivergingTail) != 1 || ce.DivergingTail[0].ID != "a-1" {
		t.Fatalf("diverging tail: %+v", ce.DivergingTail)
	}
}

// makeWorkspace returns a populated TempDir with a .rex directory,
// matching what `rex workspace init` produces structurally without
// requiring the cli package to test the wire layer.
func makeWorkspace(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".rex", "drafts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

// mkRecWithActor builds a record with a caller-chosen actor and
// workspace id so cross-workspace tests can keep their origins
// distinct.
func mkRecWithActor(id, actor, ws string) eventlog.Record {
	return eventlog.Record{
		ID:          id,
		Type:        "test.event",
		Version:     1,
		Actor:       actor,
		WorkspaceID: ws,
		Payload:     json.RawMessage(`{}`),
	}
}

func writeRaw(t *testing.T, path string, rec eventlog.Record) {
	t.Helper()
	f, err := openAppend(path)
	if err != nil {
		t.Fatalf("openAppend: %v", err)
	}
	if err := appendRaw(f, rec); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	_ = f.Close()
}

// readAllIDs scans events.log and returns the ids in order.
func readAllIDs(t *testing.T, path string) []string {
	t.Helper()
	r, err := eventlog.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()
	var out []string
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, rec.ID)
	}
	return out
}
