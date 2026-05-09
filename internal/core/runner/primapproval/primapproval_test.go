package primapproval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// approvalFixture wires a real eventlog.Writer into a sink the
// primitive can use to emit PermissionRequested. resolveAfter
// then writes a Granted/Denied event that the primitive's tail
// loop picks up.
type approvalFixture struct {
	t       *testing.T
	logPath string
	w       *eventlog.Writer
}

func newApprovalFixture(t *testing.T) *approvalFixture {
	t.Helper()
	dir := t.TempDir()
	rexDir := filepath.Join(dir, ".rex")
	if err := mkdirAllIgnoreErr(rexDir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(rexDir, "events.log")
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        logPath,
		WorkspaceID: "ws",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return &approvalFixture{t: t, logPath: logPath, w: w}
}

func (f *approvalFixture) sink() runner.EventSink { return sinkAdapter{w: f.w} }

// scheduleResolution writes a Granted or Denied event after a
// short delay so the primitive's tail loop has something to find.
func (f *approvalFixture) scheduleResolution(requestID, decision, approver, note string, after time.Duration) {
	go func() {
		time.Sleep(after)
		var (
			eventType string
			body      []byte
			err       error
		)
		if decision == string(DecisionGranted) {
			eventType = runner.EventTypePermissionGranted
			body, err = json.Marshal(runner.PermissionGrantedEvent{
				RunID: "r-1", NodeID: "approve", RequestID: requestID,
				Approver: approver, GrantedAt: time.Now().UTC(), Note: note,
			})
		} else {
			eventType = runner.EventTypePermissionDenied
			body, err = json.Marshal(runner.PermissionDeniedEvent{
				RunID: "r-1", NodeID: "approve", RequestID: requestID,
				Approver: approver, DeniedAt: time.Now().UTC(), Reason: note,
			})
		}
		if err != nil {
			f.t.Errorf("marshal: %v", err)
			return
		}
		if _, err := f.w.Append(eventType, runner.EventVersion, body); err != nil {
			f.t.Errorf("append: %v", err)
		}
	}()
}

type sinkAdapter struct{ w *eventlog.Writer }

func (s sinkAdapter) Append(eventType string, version uint32, payload json.RawMessage) error {
	_, err := s.w.Append(eventType, version, payload)
	return err
}

func mkdirAllIgnoreErr(p string) error {
	return os.MkdirAll(p, 0o755)
}

func TestApprovalGranted(t *testing.T) {
	t.Parallel()

	f := newApprovalFixture(t)
	prim := New(Options{
		LogPath:      f.logPath,
		Sink:         f.sink(),
		PollInterval: 20 * time.Millisecond,
	})

	requestID := "r-1.approval.approve"
	f.scheduleResolution(requestID, string(DecisionGranted), "alice", "looks fine", 50*time.Millisecond)

	out, err := prim.Run(context.Background(), runner.PrimitiveInput{
		RunID: "r-1",
		Node:  runner.Node{ID: "approve", Type: PrimitiveType},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got Output
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Decision != DecisionGranted || got.Approver != "alice" || got.Note != "looks fine" {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.RequestID != requestID {
		t.Fatalf("request_id: %q", got.RequestID)
	}
}

func TestApprovalDeniedReturnsError(t *testing.T) {
	t.Parallel()

	f := newApprovalFixture(t)
	prim := New(Options{
		LogPath:      f.logPath,
		Sink:         f.sink(),
		PollInterval: 20 * time.Millisecond,
	})
	requestID := "r-1.approval.approve"
	f.scheduleResolution(requestID, string(DecisionDenied), "bob", "wrong env", 50*time.Millisecond)

	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		RunID: "r-1",
		Node:  runner.Node{ID: "approve", Type: PrimitiveType},
	})
	if err == nil || !strings.Contains(err.Error(), "denied by bob") {
		t.Fatalf("expected denied error; got %v", err)
	}
}

func TestApprovalCancelledByContext(t *testing.T) {
	t.Parallel()

	f := newApprovalFixture(t)
	prim := New(Options{
		LogPath:      f.logPath,
		Sink:         f.sink(),
		PollInterval: 20 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := prim.Run(ctx, runner.PrimitiveInput{
		RunID: "r-1",
		Node:  runner.Node{ID: "approve", Type: PrimitiveType},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected ctx cancellation, got %v", err)
	}
}

func TestApprovalRequiresSink(t *testing.T) {
	t.Parallel()
	prim := New(Options{LogPath: "/dev/null"})
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "approve", Type: PrimitiveType},
	})
	if err == nil || !strings.Contains(err.Error(), "Sink is required") {
		t.Fatalf("expected Sink-required error; got %v", err)
	}
}

func TestApprovalRespectsConfigTimeout(t *testing.T) {
	t.Parallel()

	f := newApprovalFixture(t)
	prim := New(Options{
		LogPath:      f.logPath,
		Sink:         f.sink(),
		PollInterval: 20 * time.Millisecond,
	})
	cfg, _ := json.Marshal(Config{Timeout: 80 * time.Millisecond})
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		RunID: "r-1",
		Node:  runner.Node{ID: "approve", Type: PrimitiveType, Config: cfg},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded; got %v", err)
	}
}
