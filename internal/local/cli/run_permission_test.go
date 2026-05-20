package cli

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// emitPermissionRequested writes a PermissionRequestedEvent into the
// workspace's events.log so the approve/deny tests have something
// to resolve. We bypass the writer's signing path because tests
// just need the event in the log.
func emitPermissionRequested(t *testing.T, root, runID, requestID string, nodeID runner.NodeID) {
	t.Helper()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(root, ".rex", "events.log"),
		WorkspaceID: "demo",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()
	body, err := json.Marshal(runner.PermissionRequestedEvent{
		RunID:     runID,
		NodeID:    nodeID,
		RequestID: requestID,
		Tool:      "delete-prod",
		Reason:    "approval test",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := w.Append(runner.EventTypePermissionRequested, runner.EventVersion, body); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func readResolutionEvents(t *testing.T, root, eventType, runID string) []eventlog.Record {
	t.Helper()
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	var out []eventlog.Record
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if rec.Type != eventType {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// TestRunApproveResolvesPendingRequest seeds a permission request
// then resolves it via `rex run approve`. Asserts the
// PermissionGranted event lands with the right approver + note.
func TestRunApproveResolvesPendingRequest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Need a real run.started so resolveRunID can find r-x.
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "r-x"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	emitPermissionRequested(t, dir, "r-x", "r-x.req-1", "approve")

	out, err := executeCommand(t, "run", "approve", "--workspace", dir, "r-x", "--note", "looks fine")
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	if !strings.Contains(out, "approved") {
		t.Fatalf("output: %q", out)
	}

	events := readResolutionEvents(t, dir, runner.EventTypePermissionGranted, "r-x")
	if len(events) != 1 {
		t.Fatalf("want 1 granted event, got %d", len(events))
	}
	var p runner.PermissionGrantedEvent
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.RequestID != "r-x.req-1" || p.Note != "looks fine" {
		t.Fatalf("payload: %+v", p)
	}
}

func TestRunDenyResolvesPendingRequest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "r-y"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	emitPermissionRequested(t, dir, "r-y", "r-y.req-1", "approve")

	out, err := executeCommand(t, "run", "deny",
		"--workspace", dir, "r-y", "--note", "wrong env")
	if err != nil {
		t.Fatalf("deny: %v\n%s", err, out)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("output: %q", out)
	}
	events := readResolutionEvents(t, dir, runner.EventTypePermissionDenied, "r-y")
	if len(events) != 1 {
		t.Fatalf("want 1 denied event, got %d", len(events))
	}
}

// TestRunApproveErrorsWhenNoPending ensures the resolver surfaces
// a clear error when there's nothing to approve.
func TestRunApproveErrorsWhenNoPending(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "r-z"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	_, err := executeCommand(t, "run", "approve", "--workspace", dir, "r-z")
	if err == nil {
		t.Fatal("expected error when no pending requests")
	}
	if !strings.Contains(err.Error(), "no unresolved permission requests") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestRunApprovePicksMostRecentPending verifies the resolver
// chooses the latest pending request when --request-id is omitted.
func TestRunApprovePicksMostRecentPending(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "r-multi"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	emitPermissionRequested(t, dir, "r-multi", "r-multi.req-1", "approve")
	emitPermissionRequested(t, dir, "r-multi", "r-multi.req-2", "approve")

	out, err := executeCommand(t, "run", "approve", "--workspace", dir, "r-multi", "--json")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("json: %v", err)
	}
	if v["request_id"] != "r-multi.req-2" {
		t.Fatalf("expected most-recent request resolved; got %v", v)
	}
}

// TestRunApproveRespectsRequestIDFlag verifies an explicit
// --request-id targets that specific request.
func TestRunApproveRespectsRequestIDFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "r-explicit"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	emitPermissionRequested(t, dir, "r-explicit", "r-explicit.req-1", "approve")
	emitPermissionRequested(t, dir, "r-explicit", "r-explicit.req-2", "approve")

	out, err := executeCommand(t, "run", "approve",
		"--workspace", dir, "r-explicit",
		"--request-id", "r-explicit.req-1",
		"--json",
	)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("json: %v", err)
	}
	if v["request_id"] != "r-explicit.req-1" {
		t.Fatalf("explicit --request-id ignored; got %v", v)
	}
}
