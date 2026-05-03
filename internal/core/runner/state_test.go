package runner

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleDAG() DAG {
	return DAG{
		Nodes: []Node{
			{ID: "first", Type: "noop"},
			{ID: "second", Type: "noop"},
		},
		Edges: []Edge{{From: "first", To: "second"}},
	}
}

func TestNewStateSeedsPending(t *testing.T) {
	t.Parallel()

	s := NewState(sampleDAG())
	if s.Status != RunStatusPending {
		t.Fatalf("status: got %q", s.Status)
	}
	if got := len(s.Nodes); got != 2 {
		t.Fatalf("nodes: got %d", got)
	}
	for id, ns := range s.Nodes {
		if ns.Status != NodeStatusPending {
			t.Fatalf("node %q status: got %q", id, ns.Status)
		}
	}
}

func TestApplyTransitionsRunAndNodes(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	dag := sampleDAG()

	events := []any{
		RunStartedEvent{RunID: "r-1", StartedAt: now},
		NodeStartedEvent{RunID: "r-1", NodeID: "first", Attempt: 1, StartedAt: now.Add(time.Millisecond)},
		NodeSucceededEvent{RunID: "r-1", NodeID: "first", Attempt: 1, CompletedAt: now.Add(2 * time.Millisecond), Output: json.RawMessage(`{"ok":true}`)},
		NodeStartedEvent{RunID: "r-1", NodeID: "second", Attempt: 1, StartedAt: now.Add(3 * time.Millisecond)},
		NodeSucceededEvent{RunID: "r-1", NodeID: "second", Attempt: 1, CompletedAt: now.Add(4 * time.Millisecond)},
		RunCompletedEvent{RunID: "r-1", CompletedAt: now.Add(5 * time.Millisecond)},
	}

	state, err := Replay(dag, events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("run status: got %q", state.Status)
	}
	if state.RunID != "r-1" {
		t.Fatalf("run id: got %q", state.RunID)
	}
	for _, id := range []NodeID{"first", "second"} {
		ns := state.Nodes[id]
		if ns.Status != NodeStatusSucceeded {
			t.Fatalf("node %q status: got %q", id, ns.Status)
		}
	}
	if string(state.Nodes["first"].Output) != `{"ok":true}` {
		t.Fatalf("first output not preserved: %s", state.Nodes["first"].Output)
	}
}

func TestApplyHandlesFailureRetryAndAbort(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	dag := DAG{Nodes: []Node{{ID: "x", Type: "noop"}}}

	events := []any{
		RunStartedEvent{RunID: "r-2", StartedAt: now},
		NodeStartedEvent{RunID: "r-2", NodeID: "x", Attempt: 1, StartedAt: now},
		NodeFailedEvent{RunID: "r-2", NodeID: "x", Attempt: 1, FailedAt: now.Add(time.Millisecond), Error: "boom"},
		NodeRetriedEvent{RunID: "r-2", NodeID: "x", NextAttempt: 2, BackoffFor: time.Second},
		NodeStartedEvent{RunID: "r-2", NodeID: "x", Attempt: 2, StartedAt: now.Add(time.Second + time.Millisecond)},
		NodeFailedEvent{RunID: "r-2", NodeID: "x", Attempt: 2, FailedAt: now.Add(time.Second + 2*time.Millisecond), Error: "boom2"},
		RunAbortedEvent{RunID: "r-2", AbortedAt: now.Add(time.Second + 3*time.Millisecond), NodeID: "x", Reason: "exhausted retries"},
	}

	state, err := Replay(dag, events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if state.Status != RunStatusAborted {
		t.Fatalf("run status: got %q", state.Status)
	}
	if state.AbortReason != "exhausted retries" {
		t.Fatalf("abort reason: got %q", state.AbortReason)
	}
	ns := state.Nodes["x"]
	if ns.Status != NodeStatusFailed {
		t.Fatalf("node status: got %q", ns.Status)
	}
	if ns.Attempts != 2 {
		t.Fatalf("attempts: got %d want 2", ns.Attempts)
	}
	if ns.LastError != "boom2" {
		t.Fatalf("last error: got %q", ns.LastError)
	}
}

func TestApplyTracksPermissionLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	dag := DAG{Nodes: []Node{{ID: "x", Type: "noop"}}}

	events := []any{
		RunStartedEvent{RunID: "r-3", StartedAt: now},
		PermissionRequestedEvent{
			RunID: "r-3", NodeID: "x", RequestID: "req-1",
			Tool: "edit", Reason: "writing src/foo.go", RequestedAt: now,
		},
		PermissionGrantedEvent{
			RunID: "r-3", NodeID: "x", RequestID: "req-1",
			Approver: "user@example", GrantedAt: now.Add(time.Second), Note: "ok",
		},
	}

	state, err := Replay(dag, events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	ps := state.Permissions["req-1"]
	if ps == nil || !ps.Resolved || !ps.Granted {
		t.Fatalf("permission not tracked: %+v", ps)
	}
	if ps.Approver != "user@example" || ps.Note != "ok" {
		t.Fatalf("permission detail lost: %+v", ps)
	}
}

func TestApplyRejectsUnknownEvent(t *testing.T) {
	t.Parallel()

	dag := sampleDAG()
	state := NewState(dag)
	err := state.Apply(struct{}{})
	if err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("Apply: got %v want unknown event error", err)
	}
}

func TestApplyRejectsUnknownNode(t *testing.T) {
	t.Parallel()

	state := NewState(sampleDAG())
	err := state.Apply(NodeStartedEvent{NodeID: "ghost", Attempt: 1})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Apply: got %v want unknown node error", err)
	}
}

func TestApplyRejectsGrantWithoutRequest(t *testing.T) {
	t.Parallel()

	state := NewState(sampleDAG())
	err := state.Apply(PermissionGrantedEvent{RequestID: "req-stranger"})
	if err == nil || !strings.Contains(err.Error(), "req-stranger") {
		t.Fatalf("Apply: got %v want unknown request error", err)
	}
}
