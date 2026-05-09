package runtask

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/primapproval"
	"github.com/asabla/rex/internal/core/runner/primbranch"
	"github.com/asabla/rex/internal/core/runner/primparallel"
	"github.com/asabla/rex/internal/core/runner/primshell"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// These tests exercise the three new DAG primitives end-to-end
// against the real on-disk surface used by every CLI run:
// runtask.Open builds the signed eventlog.Writer + hooks
// dispatcher + search indexer, the executor walks the DAG
// against that writer's sink, and the resulting events.log is
// read back to assert outcomes. The CLI doesn't yet have a
// surface for launching multi-node DAGs (no workflow YAML
// loader), so this is the closest E2E surface to "what production
// would do" without simulating the harness layer.

// setupE2EWorkspace creates a tempdir workspace with the
// minimum metadata Open requires (.rex/workspace.yaml). It also
// scopes REX_IDENTITY_DIR to a tempdir so the auto-load signer
// path doesn't write into the user's real config.
func setupE2EWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	rexDir := filepath.Join(root, ".rex")
	if err := os.MkdirAll(rexDir, 0o755); err != nil {
		t.Fatalf("mkdir .rex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rexDir, "workspace.yaml"),
		[]byte("id: ws-e2e\n"), 0o644); err != nil {
		t.Fatalf("write workspace.yaml: %v", err)
	}
	t.Setenv("REX_IDENTITY_DIR", filepath.Join(t.TempDir(), "identity"))
	ws, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws, root
}

// buildE2ERegistry mirrors the primitive registration order the
// production StartShellRun uses, so integration tests share the
// same registry as live runs.
func buildE2ERegistry(ws *Workspace, sink runner.EventSink) *runner.PrimitiveRegistry {
	reg := runner.NewPrimitiveRegistry()
	reg.Register(primshell.PrimitiveType, primshell.New(primshell.Options{WorkspaceDir: ws.Root}))
	reg.Register(primbranch.PrimitiveType, primbranch.New())
	reg.Register(primapproval.PrimitiveType, primapproval.New(primapproval.Options{
		WorkspaceRoot: ws.Root,
		Sink:          sink,
	}))
	reg.Register(primparallel.PrimitiveType, primparallel.New(primparallel.Options{
		Registry: reg,
	}))
	return reg
}

// runE2EDAG drives one DAG to completion through the real
// eventlog.Writer composition Open built. Returns the engine's
// final RunState (same surface tests would see via Replay over
// events.log).
func runE2EDAG(ctx context.Context, t *testing.T, ws *Workspace, dag runner.DAG, runID string) (*runner.RunState, error) {
	t.Helper()
	sink := &writerSink{w: ws.Writer}
	reg := buildE2ERegistry(ws, sink)
	exec, err := runner.NewExecutor(runner.ExecConfig{
		RunID:    runID,
		DAG:      dag,
		Sink:     sink,
		Registry: reg,
	})
	if err != nil {
		return nil, err
	}
	return exec.Run(ctx)
}

// readEvents pulls every event for runID off the on-disk log so
// assertions can match against the persisted record (not just
// the in-memory sink).
func readEvents(t *testing.T, root, runID string) []eventlog.Record {
	t.Helper()
	r, err := eventlog.OpenReader(EventLogPath(root))
	if err != nil {
		t.Fatalf("open reader: %v", err)
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
		// Filter on RunID by peeking at the payload — every
		// runner-emitted event carries one. Records without a
		// run_id (e.g. workspace lifecycle) are skipped.
		var probe struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(rec.Payload, &probe); err != nil {
			continue
		}
		if probe.RunID != runID {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func eventTypes(records []eventlog.Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Type
	}
	return out
}

// TestE2EBranchPredicateSkipsUnmatchedDownstream wires a real
// shell node feeding two downstream shells via predicate-gated
// edges (exit_code_eq:0 vs exit_code_eq:1). With the upstream
// exiting 0, the matching branch must succeed and the
// non-matching branch must emit NodeSkipped — exactly the
// PRIM.5 contract, but proved against the real executor + sink.
func TestE2EBranchPredicateSkipsUnmatchedDownstream(t *testing.T) {
	// no t.Parallel — uses t.Setenv
	ws, root := setupE2EWorkspace(t)

	cfgEcho := mustMarshal(t, primshell.Config{Command: []string{"sh", "-c", "true"}})
	cfgYes := mustMarshal(t, primshell.Config{Command: []string{"sh", "-c", "echo yes"}})
	cfgNo := mustMarshal(t, primshell.Config{Command: []string{"sh", "-c", "echo no"}})

	dag := runner.DAG{
		Nodes: []runner.Node{
			{ID: "emit", Type: primshell.PrimitiveType, Config: cfgEcho},
			{ID: "took-zero", Type: primshell.PrimitiveType, Config: cfgYes},
			{ID: "took-one", Type: primshell.PrimitiveType, Config: cfgNo},
		},
		Edges: []runner.Edge{
			{From: "emit", To: "took-zero", Predicate: `{"kind":"exit_code_eq","value":0}`},
			{From: "emit", To: "took-one", Predicate: `{"kind":"exit_code_eq","value":1}`},
		},
	}

	state, err := runE2EDAG(context.Background(), t, ws, dag, "r-branch")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != runner.RunStatusCompleted {
		t.Fatalf("run status: %q", state.Status)
	}
	if state.Nodes["took-zero"].Status != runner.NodeStatusSucceeded {
		t.Fatalf("took-zero status: %q", state.Nodes["took-zero"].Status)
	}
	if state.Nodes["took-one"].Status != runner.NodeStatusSkipped {
		t.Fatalf("took-one status: %q (expected skipped)", state.Nodes["took-one"].Status)
	}

	persisted := readEvents(t, root, "r-branch")
	if !containsType(persisted, runner.EventTypeNodeSkipped) {
		t.Fatalf("expected NodeSkipped in events.log; got %v", eventTypes(persisted))
	}
}

// TestE2EParallelFanOutFanInFromRealExecutor exercises PRIM.6
// against the full runtask stack: parallel-of-three-shells with
// 80ms sleeps must finish well under serial time and all three
// children's outputs must land on the parent NodeSucceededEvent.
func TestE2EParallelFanOutFanInFromRealExecutor(t *testing.T) {
	// no t.Parallel — uses t.Setenv
	ws, root := setupE2EWorkspace(t)

	// Per-child sleep is 200ms × 3 = 600ms serial baseline. Setting
	// the assertion threshold to 500ms gives us a clear "concurrent
	// must be faster than serial" signal even on slow CI runners
	// where each `sh -c` spawn carries 50–100ms of overhead. The
	// previous 80ms × 3 / 250ms threshold was too tight: serial
	// overhead alone exceeded 250ms on hosted runners (observed
	// 430ms) and tripped the assertion without actually proving
	// the run was serial.
	mkShell := func(id string) primparallel.ChildSpec {
		cfg := mustMarshal(t, primshell.Config{
			Command: []string{"sh", "-c", "sleep 0.2; echo " + id},
		})
		return primparallel.ChildSpec{ID: id, Type: primshell.PrimitiveType, Config: cfg}
	}
	parallelCfg := mustMarshal(t, primparallel.Config{
		Children: []primparallel.ChildSpec{mkShell("a"), mkShell("b"), mkShell("c")},
	})

	dag := runner.DAG{
		Nodes: []runner.Node{
			{ID: "fan", Type: primparallel.PrimitiveType, Config: parallelCfg},
		},
	}

	start := time.Now()
	state, err := runE2EDAG(context.Background(), t, ws, dag, "r-parallel")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected concurrent execution (≪ 600ms serial baseline); elapsed=%s", elapsed)
	}
	if state.Status != runner.RunStatusCompleted {
		t.Fatalf("run status: %q", state.Status)
	}

	// Walk persisted events, locate NodeSucceeded for "fan",
	// decode its Output as primparallel.Output, and assert all
	// three children landed succeeded.
	var succeeded *runner.NodeSucceededEvent
	for _, rec := range readEvents(t, root, "r-parallel") {
		if rec.Type != runner.EventTypeNodeSucceeded {
			continue
		}
		var ev runner.NodeSucceededEvent
		if err := json.Unmarshal(rec.Payload, &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.NodeID == "fan" {
			ev := ev
			succeeded = &ev
			break
		}
	}
	if succeeded == nil {
		t.Fatal("no NodeSucceeded event for fan node")
	}
	var out primparallel.Output
	if err := json.Unmarshal(succeeded.Output, &out); err != nil {
		t.Fatalf("decode parallel output: %v", err)
	}
	if out.SucceededCount != 3 || out.FailedCount != 0 {
		t.Fatalf("parallel summary: %+v", out)
	}
	got := map[string]bool{}
	for _, c := range out.Children {
		got[c.ID] = c.Status == "succeeded"
	}
	for _, id := range []string{"a", "b", "c"} {
		if !got[id] {
			t.Fatalf("child %q not succeeded; full: %+v", id, out.Children)
		}
	}
}

// TestE2EHumanApprovalResolvedFromExternalProcess proves PRIM.4's
// cross-process IPC: the primitive emits PermissionRequested
// through the run's sink, an "external" goroutine (simulating a
// second `rex run approve` invocation) opens a fresh writer
// against the same events.log and writes PermissionGranted; the
// primitive's tail loop picks it up and the run completes with
// the approver baked into the parent NodeSucceeded payload.
func TestE2EHumanApprovalResolvedFromExternalProcess(t *testing.T) {
	// no t.Parallel — uses t.Setenv
	ws, root := setupE2EWorkspace(t)

	approvalCfg := mustMarshal(t, primapproval.Config{
		Tool:   "delete-prod",
		Reason: "integration test",
	})
	dag := runner.DAG{
		Nodes: []runner.Node{
			{ID: "approve", Type: primapproval.PrimitiveType, Config: approvalCfg},
		},
	}

	// External resolver: opens its own writer, polls the log
	// for PermissionRequested for our run, then writes
	// PermissionGranted — exactly what `rex run approve` does.
	resolverDone := make(chan error, 1)
	go func() {
		resolverDone <- resolvePendingApprovalAfterDelay(root, "r-approve", "alice", "looks fine", 1500*time.Millisecond)
	}()

	state, err := runE2EDAG(context.Background(), t, ws, dag, "r-approve")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rerr := <-resolverDone; rerr != nil {
		t.Fatalf("resolver: %v", rerr)
	}
	if state.Status != runner.RunStatusCompleted {
		t.Fatalf("run status: %q", state.Status)
	}

	// The parent NodeSucceededEvent must carry the granted
	// decision + approver — that's what audit consumers see.
	var found bool
	for _, rec := range readEvents(t, root, "r-approve") {
		if rec.Type != runner.EventTypeNodeSucceeded {
			continue
		}
		var ev runner.NodeSucceededEvent
		if err := json.Unmarshal(rec.Payload, &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ev.NodeID != "approve" {
			continue
		}
		var out primapproval.Output
		if err := json.Unmarshal(ev.Output, &out); err != nil {
			t.Fatalf("decode approval output: %v", err)
		}
		if out.Decision != primapproval.DecisionGranted {
			t.Fatalf("decision: %q", out.Decision)
		}
		if out.Approver != "alice" || out.Note != "looks fine" {
			t.Fatalf("approver/note: %+v", out)
		}
		found = true
	}
	if !found {
		t.Fatal("no approve NodeSucceeded event in events.log")
	}
}

// resolvePendingApprovalAfterDelay simulates `rex run approve`
// without going through cobra: opens a separate writer at the
// same events.log path, polls until a PermissionRequested for
// runID appears, then writes PermissionGranted. The time budget
// is generous enough to cover slow CI machines without making
// the success path artificially long.
func resolvePendingApprovalAfterDelay(root, runID, approver, note string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	logPath := EventLogPath(root)

	var requestID string
	var nodeID runner.NodeID
	for time.Now().Before(deadline) {
		req, found, err := findPendingPermission(logPath, runID)
		if err != nil {
			return err
		}
		if found {
			requestID = req.RequestID
			nodeID = req.NodeID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requestID == "" {
		return errors.New("resolver: timed out waiting for PermissionRequested")
	}

	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        logPath,
		WorkspaceID: "ws-e2e",
	})
	if err != nil {
		return err
	}
	defer w.Close()
	body, err := json.Marshal(runner.PermissionGrantedEvent{
		RunID:     runID,
		NodeID:    nodeID,
		RequestID: requestID,
		Approver:  approver,
		GrantedAt: time.Now().UTC(),
		Note:      note,
	})
	if err != nil {
		return err
	}
	if _, err := w.Append(runner.EventTypePermissionGranted, runner.EventVersion, body); err != nil {
		return err
	}
	return nil
}

// findPendingPermission scans logPath for an unresolved
// PermissionRequested event for runID. Used by the test's
// external resolver and validates that the cross-process IPC
// surface is what a real `rex run approve` would key off.
func findPendingPermission(logPath, runID string) (runner.PermissionRequestedEvent, bool, error) {
	r, err := eventlog.OpenReader(logPath)
	if err != nil {
		return runner.PermissionRequestedEvent{}, false, nil
	}
	defer r.Close()
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			return runner.PermissionRequestedEvent{}, false, nil
		}
		if err != nil {
			return runner.PermissionRequestedEvent{}, false, err
		}
		if rec.Type != runner.EventTypePermissionRequested {
			continue
		}
		var ev runner.PermissionRequestedEvent
		if err := json.Unmarshal(rec.Payload, &ev); err != nil {
			continue
		}
		if ev.RunID == runID {
			return ev, true, nil
		}
	}
}

// containsType reports whether the events list includes one of
// the named type — keeps the assertions tight.
func containsType(records []eventlog.Record, t string) bool {
	for _, r := range records {
		if r.Type == t {
			return true
		}
	}
	return false
}

// mustMarshal is the test's "panic-if-broken" wrapper around
// json.Marshal — every body it builds is from a typed struct,
// so failures are programming errors, not test inputs.
func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	if !strings.HasPrefix(string(body), "{") && !strings.HasPrefix(string(body), "[") {
		t.Fatalf("expected object or array body, got %s", body)
	}
	return body
}
