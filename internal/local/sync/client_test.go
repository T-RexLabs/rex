//go:build central_e2e

package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/rex-centralized/central/server"
)

func newTestServer(t *testing.T) (*server.Server, *httptest.Server) {
	t.Helper()
	srv, err := server.New(server.Options{})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

func mkRec(id string) eventlog.Record {
	return eventlog.Record{
		ID:          id,
		Type:        "test.event",
		Version:     1,
		Actor:       "l-aaaaaaaaaaaaaaaa",
		WorkspaceID: "ws-1",
		Payload:     json.RawMessage(`{}`),
	}
}

func TestClientStateRoundTrip(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	c := NewClient(hs.URL)
	state, err := c.State(context.Background())
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.ProtocolVersion != 1 {
		t.Fatalf("version: got %d", state.ProtocolVersion)
	}
	if state.Fingerprint == "" {
		t.Fatal("fingerprint should be set")
	}
}

func TestClientPushHappyPath(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	c := NewClient(hs.URL)
	res, err := c.Push(context.Background(), "", []eventlog.Record{mkRec("e1"), mkRec("e2")})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.HeadID != "e2" {
		t.Fatalf("head: got %q", res.HeadID)
	}
	if res.Accepted != 2 || res.Duplicates != 0 {
		t.Fatalf("counts: %+v", res)
	}
}

func TestClientPushConflictReturnsTypedError(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	c := NewClient(hs.URL)
	_, _ = srv.Store().Append(context.Background(), mkRec("seed"))

	_, err := c.Push(context.Background(), "wrong", []eventlog.Record{mkRec("e1")})
	if !IsConflict(err) {
		t.Fatalf("got %v want ConflictError", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("As: %v", err)
	}
	if ce.ServerHead != "seed" {
		t.Fatalf("server head: got %q", ce.ServerHead)
	}
}

func TestClientPullObservesAllEvents(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = srv.Store().Append(context.Background(), mkRec(id))
	}
	c := NewClient(hs.URL)
	var got []string
	n, err := c.Pull(context.Background(), "", func(rec eventlog.Record) error {
		got = append(got, rec.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if n != 3 || len(got) != 3 {
		t.Fatalf("count: got %d (%v)", n, got)
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i] != want {
			t.Fatalf("at %d: got %q want %q", i, got[i], want)
		}
	}
}

func TestClientPullSinceCursorReturnsTail(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = srv.Store().Append(context.Background(), mkRec(id))
	}
	c := NewClient(hs.URL)
	var got []string
	_, err := c.Pull(context.Background(), "a", func(rec eventlog.Record) error {
		got = append(got, rec.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("tail: %v", got)
	}
}

func TestClientPullCallbackErrorAborts(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = srv.Store().Append(context.Background(), mkRec(id))
	}
	c := NewClient(hs.URL)
	abort := errors.New("abort")
	count, err := c.Pull(context.Background(), "", func(rec eventlog.Record) error {
		if rec.ID == "b" {
			return abort
		}
		return nil
	})
	if !errors.Is(err, abort) {
		t.Fatalf("err: got %v want abort", err)
	}
	if count != 1 {
		t.Fatalf("count before abort: got %d want 1", count)
	}
}

func TestSyncPushesLocalEventsToFreshServer(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")

	f, _ := openAppend(logPath)
	for _, id := range []string{"local-1", "local-2"} {
		if err := appendRaw(f, mkRec(id)); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	_ = f.Close()

	c := NewClient(hs.URL)
	res, err := c.Sync(context.Background(), wsRoot, "primary", logPath)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Pulled != 0 {
		t.Fatalf("pulled: got %d want 0", res.Pulled)
	}
	if res.Push.Accepted != 2 {
		t.Fatalf("push accepted: got %d want 2", res.Push.Accepted)
	}
	if res.Push.HeadID != "local-2" {
		t.Fatalf("push head: got %q", res.Push.HeadID)
	}

	wm, err := LoadWatermark(wsRoot, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if wm.LastAckedEventID != "local-2" {
		t.Fatalf("watermark: got %q", wm.LastAckedEventID)
	}
}

func TestSyncDivergenceReturnsConflict(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	_, _ = srv.Store().Append(context.Background(), mkRec("server-only"))

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")
	f, _ := openAppend(logPath)
	if err := appendRaw(f, mkRec("local-1")); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	_ = f.Close()

	c := NewClient(hs.URL)
	_, err := c.Sync(context.Background(), wsRoot, "primary", logPath)
	if !IsConflict(err) {
		t.Fatalf("got %v want ConflictError", err)
	}
}

func TestSyncNoLocalChangesStillPulls(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	_, _ = srv.Store().Append(context.Background(), mkRec("server-only"))

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")

	c := NewClient(hs.URL)
	res, err := c.Sync(context.Background(), wsRoot, "primary", logPath)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("pulled: got %d want 1", res.Pulled)
	}
	if res.Push.Accepted != 0 {
		t.Fatalf("push accepted: got %d want 0", res.Push.Accepted)
	}
}

func TestReadEventsAfterUnknownSinceErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	f, _ := openAppend(logPath)
	_ = appendRaw(f, mkRec("a"))
	_ = f.Close()

	_, err := readEventsAfter(logPath, "nope")
	if !errors.Is(err, ErrUnknownSince) {
		t.Fatalf("got %v want ErrUnknownSince", err)
	}
}

func TestReadEventsAfterEmptyPathReturnsNil(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	got, err := readEventsAfter(logPath, "")
	if err != nil {
		t.Fatalf("readEventsAfter: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len: got %d want 0", len(got))
	}
}
