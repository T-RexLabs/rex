package audit

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

func newTestWriter(t *testing.T) *eventlog.Writer {
	t.Helper()
	dir := t.TempDir()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(dir, "events.log"),
		WorkspaceID: "ws-test",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestIsAuditEventCoversWorkspaceCreated(t *testing.T) {
	t.Parallel()

	if !IsAuditEvent(EventTypeWorkspaceCreated) {
		t.Fatalf("%s should be audit-class", EventTypeWorkspaceCreated)
	}
}

func TestIsAuditEventCoversRunnerEvents(t *testing.T) {
	t.Parallel()

	for _, tp := range []string{
		runner.EventTypeRunStarted,
		runner.EventTypeRunCompleted,
		runner.EventTypeRunCancelled,
		runner.EventTypeRunAborted,
		runner.EventTypeNodeStarted,
		runner.EventTypeNodeSucceeded,
		runner.EventTypeNodeFailed,
		runner.EventTypeNodeRetried,
		runner.EventTypePermissionRequested,
		runner.EventTypePermissionGranted,
		runner.EventTypePermissionDenied,
	} {
		if !IsAuditEvent(tp) {
			t.Errorf("runner event %q should be audit-class", tp)
		}
	}
}

func TestIsAuditEventRejectsUnknown(t *testing.T) {
	t.Parallel()

	if IsAuditEvent("totally.invented.event") {
		t.Fatal("unknown event must not be classified as audit")
	}
}

func TestEventTypesIsSortedAndDeduped(t *testing.T) {
	t.Parallel()

	got := EventTypes()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("EventTypes not sorted: %v", got)
	}
	seen := map[string]struct{}{}
	for _, t2 := range got {
		if _, dup := seen[t2]; dup {
			t.Fatalf("duplicate event type %q in registry", t2)
		}
		seen[t2] = struct{}{}
	}
}

func TestAppenderAcceptsAuditEvent(t *testing.T) {
	t.Parallel()

	w := newTestWriter(t)
	app := NewAppender(w)
	rec, err := app.Append(EventTypeWorkspaceCreated, WorkspaceCreatedEvent{
		WorkspaceID: "demo",
		Name:        "Demo",
		Path:        "/tmp/demo",
		CreatedAt:   "2026-05-03T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rec.Type != EventTypeWorkspaceCreated {
		t.Fatalf("type: got %q want %q", rec.Type, EventTypeWorkspaceCreated)
	}
	if rec.Version != EventVersion {
		t.Fatalf("version: got %d want %d", rec.Version, EventVersion)
	}
	var body WorkspaceCreatedEvent
	if err := json.Unmarshal(rec.Payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if body.WorkspaceID != "demo" {
		t.Fatalf("workspace id round-trip: %+v", body)
	}
}

func TestAppenderRejectsNonAuditEvent(t *testing.T) {
	t.Parallel()

	w := newTestWriter(t)
	app := NewAppender(w)
	_, err := app.Append("rogue.event", map[string]string{"x": "y"})
	if !errors.Is(err, ErrNotAuditEvent) {
		t.Fatalf("got %v want ErrNotAuditEvent", err)
	}
}

func TestAppenderWritesThroughToReader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path: path, WorkspaceID: "ws",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	app := NewAppender(w)
	if _, err := app.Append(EventTypeWorkspaceCreated, WorkspaceCreatedEvent{WorkspaceID: "ws", Name: "Ws"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := eventlog.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	rec, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rec.Type != EventTypeWorkspaceCreated {
		t.Fatalf("type: got %q", rec.Type)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after one record, got %v", err)
	}
}
