package runner

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// writeCancelEvent appends a run.cancellation_requested event for
// runID directly to events.log. Used by the watcher tests to
// simulate `rex run cancel` from a separate process.
func writeCancelEvent(t *testing.T, path, runID, reason string) {
	t.Helper()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{Path: path, WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()
	body, err := json.Marshal(RunCancellationRequestedEvent{
		RunID:       runID,
		RequestedAt: time.Now().UTC(),
		Reason:      reason,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := w.Append(EventTypeRunCancellationRequested, EventVersion, body); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestWatchForCancelFiresOnMatchingEvent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")

	// Pre-write the event so the very first scan-pass picks it
	// up — deterministic, no goroutine-timing flakiness.
	writeCancelEvent(t, logPath, "run-X", "user requested")

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		WatchForCancel(ctx, logPath, "run-X", cancel)
		close(done)
	}()
	select {
	case <-ctx.Done():
		// good — cancel fired
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not cancel within 2s")
	}
	<-done

	cause := context.Cause(ctx)
	var cr *CancelRequestedError
	if !errors.As(cause, &cr) {
		t.Fatalf("expected CancelRequestedError cause, got %T %v", cause, cause)
	}
	if cr.Reason != "user requested" {
		t.Fatalf("reason: %q", cr.Reason)
	}
}

func TestWatchForCancelIgnoresOtherRunIDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")

	// Cancel for a different run id — our watcher should not
	// fire.
	writeCancelEvent(t, logPath, "run-OTHER", "different run")

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stopCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	WatchForCancel(stopCtx, logPath, "run-X", cancel)

	if context.Cause(ctx) != nil {
		t.Fatalf("watcher should not have cancelled: %v", context.Cause(ctx))
	}
}

func TestWatchForCancelExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		WatchForCancel(ctx, logPath, "run-X", cancel)
		close(done)
	}()
	cancel(errors.New("test stopping watcher"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit on ctx cancel")
	}
}

func TestCancelRequestedErrorErrorMessage(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		c    *CancelRequestedError
		want string
	}{
		"nil":      {nil, "run cancellation requested"},
		"empty":    {&CancelRequestedError{}, "run cancellation requested"},
		"reasoned": {&CancelRequestedError{Reason: "redo"}, "run cancellation requested: redo"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.c.Error(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
