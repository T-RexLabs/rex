package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestRegistry() *PrimitiveRegistry {
	r := NewPrimitiveRegistry()
	r.Register("noop", PrimitiveFunc(func(ctx context.Context, in PrimitiveInput) (PrimitiveOutput, error) {
		return PrimitiveOutput{Output: json.RawMessage(`{"id":"` + string(in.Node.ID) + `"}`)}, nil
	}))
	return r
}

// fakeNow returns a deterministic monotonic clock for tests.
func fakeNow() func() time.Time {
	var (
		mu sync.Mutex
		n  int64
	)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := time.Unix(1700000000, 0).Add(time.Duration(n) * time.Millisecond)
		n++
		return t
	}
}

func TestExecutorLinearChain(t *testing.T) {
	t.Parallel()

	dag := DAG{
		Nodes: []Node{
			{ID: "first", Type: "noop"},
			{ID: "second", Type: "noop"},
			{ID: "third", Type: "noop"},
		},
		Edges: []Edge{
			{From: "first", To: "second"},
			{From: "second", To: "third"},
		},
	}
	sink := &InMemorySink{}
	exec, err := NewExecutor(ExecConfig{
		RunID:    "r-1",
		DAG:      dag,
		Sink:     sink,
		Registry: newTestRegistry(),
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("status: got %q", state.Status)
	}
	want := []string{
		EventTypeRunStarted,
		EventTypeNodeStarted, EventTypeNodeSucceeded,
		EventTypeNodeStarted, EventTypeNodeSucceeded,
		EventTypeNodeStarted, EventTypeNodeSucceeded,
		EventTypeRunCompleted,
	}
	got := sink.Types()
	if len(got) != len(want) {
		t.Fatalf("event count: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExecutorRetriesAndAborts(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	reg := NewPrimitiveRegistry()
	reg.Register("flaky", PrimitiveFunc(func(_ context.Context, _ PrimitiveInput) (PrimitiveOutput, error) {
		attempts.Add(1)
		return PrimitiveOutput{}, errors.New("boom")
	}))

	dag := DAG{
		Nodes: []Node{{ID: "x", Type: "flaky", Retry: RetryPolicy{MaxAttempts: 3, Backoff: 5 * time.Millisecond}}},
	}

	var slept []time.Duration
	sink := &InMemorySink{}
	exec, err := NewExecutor(ExecConfig{
		RunID:    "r-2",
		DAG:      dag,
		Sink:     sink,
		Registry: reg,
		Now:      fakeNow(),
		Sleep: func(d time.Duration) {
			slept = append(slept, d)
		},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != RunStatusAborted {
		t.Fatalf("status: got %q", state.Status)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts: got %d want 3", attempts.Load())
	}
	if len(slept) != 2 {
		t.Fatalf("slept %d times, want 2 (between three attempts): %v", len(slept), slept)
	}
	if slept[0] != 5*time.Millisecond || slept[1] != 10*time.Millisecond {
		t.Fatalf("linear backoff lost: got %v want [5ms 10ms]", slept)
	}
	wantTypes := []string{
		EventTypeRunStarted,
		EventTypeNodeStarted, EventTypeNodeFailed, EventTypeNodeRetried,
		EventTypeNodeStarted, EventTypeNodeFailed, EventTypeNodeRetried,
		EventTypeNodeStarted, EventTypeNodeFailed,
		EventTypeRunAborted,
	}
	got := sink.Types()
	if len(got) != len(wantTypes) {
		t.Fatalf("event count: got %d want %d (%v)", len(got), len(wantTypes), got)
	}
	for i, w := range wantTypes {
		if got[i] != w {
			t.Fatalf("event %d: got %q want %q", i, got[i], w)
		}
	}
}

func TestExecutorRetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	reg := NewPrimitiveRegistry()
	reg.Register("flaky-2", PrimitiveFunc(func(_ context.Context, _ PrimitiveInput) (PrimitiveOutput, error) {
		if calls.Add(1) < 2 {
			return PrimitiveOutput{}, errors.New("first call boom")
		}
		return PrimitiveOutput{Output: json.RawMessage(`{"ok":true}`)}, nil
	}))
	dag := DAG{
		Nodes: []Node{{ID: "x", Type: "flaky-2", Retry: RetryPolicy{MaxAttempts: 3, Backoff: time.Millisecond}}},
	}
	exec, err := NewExecutor(ExecConfig{
		RunID:    "r-3",
		DAG:      dag,
		Sink:     &InMemorySink{},
		Registry: reg,
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("status: got %q", state.Status)
	}
	if string(state.Nodes["x"].Output) != `{"ok":true}` {
		t.Fatalf("output: got %s", state.Nodes["x"].Output)
	}
}

func TestExecutorCancellation(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	reg := NewPrimitiveRegistry()
	reg.Register("blocker", PrimitiveFunc(func(ctx context.Context, _ PrimitiveInput) (PrimitiveOutput, error) {
		close(gate)
		<-ctx.Done()
		return PrimitiveOutput{}, ctx.Err()
	}))
	dag := DAG{Nodes: []Node{{ID: "x", Type: "blocker", Retry: RetryPolicy{MaxAttempts: 1}}}}

	sink := &InMemorySink{}
	exec, err := NewExecutor(ExecConfig{
		RunID:    "r-4",
		DAG:      dag,
		Sink:     sink,
		Registry: reg,
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan *RunState, 1)
	go func() {
		state, _ := exec.Run(ctx)
		doneCh <- state
	}()

	<-gate
	cancel()

	select {
	case state := <-doneCh:
		if state.Status != RunStatusCancelled {
			t.Fatalf("status: got %q want Cancelled", state.Status)
		}
		if !strings.Contains(state.AbortReason, "context") {
			t.Fatalf("Reason should mention context: %q", state.AbortReason)
		}
		// A cancelled run must not have emitted RunAborted on top.
		for _, e := range sink.Events {
			if e.Type == EventTypeRunAborted {
				t.Fatalf("cancelled run also emitted run.aborted")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestExecutorEventsReplayToSameState(t *testing.T) {
	t.Parallel()

	dag := DAG{
		Nodes: []Node{
			{ID: "first", Type: "noop"},
			{ID: "second", Type: "noop"},
		},
		Edges: []Edge{{From: "first", To: "second"}},
	}
	sink := &InMemorySink{}
	exec, err := NewExecutor(ExecConfig{
		RunID:    "r-5",
		DAG:      dag,
		Sink:     sink,
		Registry: newTestRegistry(),
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	live, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	registry := newDecoderRegistry()
	decoded := decodeAll(t, registry, sink.Events)
	replayed, err := Replay(dag, decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// Compare by JSON to avoid pointer-graph equality fuss.
	a, _ := json.Marshal(struct {
		Status RunStatus
		Nodes  map[NodeID]*NodeState
	}{live.Status, live.Nodes})
	b, _ := json.Marshal(struct {
		Status RunStatus
		Nodes  map[NodeID]*NodeState
	}{replayed.Status, replayed.Nodes})
	if string(a) != string(b) {
		t.Fatalf("replayed state diverges:\n live: %s\nreplay: %s", a, b)
	}
}

func TestExecutorRejectsDAGWithUnknownPrimitive(t *testing.T) {
	t.Parallel()

	dag := DAG{Nodes: []Node{{ID: "x", Type: "missing"}}}
	_, err := NewExecutor(ExecConfig{
		RunID:    "r",
		DAG:      dag,
		Sink:     &InMemorySink{},
		Registry: newTestRegistry(),
	})
	if !errors.Is(err, ErrPrimitiveUnknown) {
		t.Fatalf("NewExecutor: got %v want ErrPrimitiveUnknown", err)
	}
}

func TestExecutorBranchedDAG(t *testing.T) {
	t.Parallel()

	dag := DAG{
		Nodes: []Node{
			{ID: "root", Type: "noop"},
			{ID: "left", Type: "noop"},
			{ID: "right", Type: "noop"},
			{ID: "join", Type: "noop"},
		},
		Edges: []Edge{
			{From: "root", To: "left"},
			{From: "root", To: "right"},
			{From: "left", To: "join"},
			{From: "right", To: "join"},
		},
	}
	sink := &InMemorySink{}
	exec, err := NewExecutor(ExecConfig{
		RunID:    "r-6",
		DAG:      dag,
		Sink:     sink,
		Registry: newTestRegistry(),
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("status: got %q", state.Status)
	}
	for _, id := range []NodeID{"root", "left", "right", "join"} {
		if state.Nodes[id].Status != NodeStatusSucceeded {
			t.Fatalf("node %q: got %q", id, state.Nodes[id].Status)
		}
	}

	// Locate the join's NodeStarted entry: it must come after both
	// left.Succeeded and right.Succeeded.
	idx := func(t string, after int) int {
		for i := after; i < len(sink.Events); i++ {
			if sink.Events[i].Type == t {
				return i
			}
		}
		return -1
	}
	leftSucc := -1
	rightSucc := -1
	for i, e := range sink.Events {
		if e.Type != EventTypeNodeSucceeded {
			continue
		}
		var body NodeSucceededEvent
		_ = json.Unmarshal(e.Payload, &body)
		switch body.NodeID {
		case "left":
			leftSucc = i
		case "right":
			rightSucc = i
		}
	}
	if leftSucc < 0 || rightSucc < 0 {
		t.Fatalf("missing succeeded events: left=%d right=%d", leftSucc, rightSucc)
	}
	joinStart := -1
	for i, e := range sink.Events {
		if e.Type != EventTypeNodeStarted {
			continue
		}
		var body NodeStartedEvent
		_ = json.Unmarshal(e.Payload, &body)
		if body.NodeID == "join" {
			joinStart = i
			break
		}
	}
	if joinStart < 0 {
		t.Fatalf("join.started not found")
	}
	if joinStart < leftSucc || joinStart < rightSucc {
		t.Fatalf("join started before its dependencies completed: left=%d right=%d join=%d", leftSucc, rightSucc, joinStart)
	}
	_ = idx
}

// newBranchTestRegistry registers a "noop-output" primitive that
// returns whatever JSON output the test wires into Node.Config so
// predicates downstream have something deterministic to evaluate
// against.
func newBranchTestRegistry() *PrimitiveRegistry {
	r := NewPrimitiveRegistry()
	r.Register("noop", PrimitiveFunc(func(ctx context.Context, in PrimitiveInput) (PrimitiveOutput, error) {
		return PrimitiveOutput{Output: json.RawMessage(`{"id":"` + string(in.Node.ID) + `"}`)}, nil
	}))
	r.Register("noop-output", PrimitiveFunc(func(ctx context.Context, in PrimitiveInput) (PrimitiveOutput, error) {
		out := in.Node.Config
		if len(out) == 0 {
			out = json.RawMessage("{}")
		}
		return PrimitiveOutput{Output: out}, nil
	}))
	return r
}

func TestExecutorPredicateGatesEdges(t *testing.T) {
	t.Parallel()

	// root → {left, right}; predicate on root→left matches, predicate
	// on root→right does not. Expect: left runs, right is skipped.
	dag := DAG{
		Nodes: []Node{
			{ID: "root", Type: "noop-output", Config: json.RawMessage(`{"branch":"left"}`)},
			{ID: "left", Type: "noop"},
			{ID: "right", Type: "noop"},
		},
		Edges: []Edge{
			{From: "root", To: "left", Predicate: `{"kind":"path_eq","path":"branch","value":"left"}`},
			{From: "root", To: "right", Predicate: `{"kind":"path_eq","path":"branch","value":"right"}`},
		},
	}
	sink := &InMemorySink{}
	exec, err := NewExecutor(ExecConfig{
		RunID: "r-branch", DAG: dag, Sink: sink,
		Registry: newBranchTestRegistry(),
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("status: %q", state.Status)
	}
	if state.Nodes["root"].Status != NodeStatusSucceeded {
		t.Fatalf("root: %q", state.Nodes["root"].Status)
	}
	if state.Nodes["left"].Status != NodeStatusSucceeded {
		t.Fatalf("left should have run: %q", state.Nodes["left"].Status)
	}
	if state.Nodes["right"].Status != NodeStatusSkipped {
		t.Fatalf("right should be skipped: %q", state.Nodes["right"].Status)
	}
	// At least one node.skipped event landed.
	var sawSkipped bool
	for _, e := range sink.Events {
		if e.Type == EventTypeNodeSkipped {
			sawSkipped = true
			break
		}
	}
	if !sawSkipped {
		t.Fatal("expected node.skipped event in the sink")
	}
}

func TestExecutorPredicateNeverSkipsAllDownstream(t *testing.T) {
	t.Parallel()

	dag := DAG{
		Nodes: []Node{
			{ID: "root", Type: "noop"},
			{ID: "downstream", Type: "noop"},
		},
		Edges: []Edge{
			{From: "root", To: "downstream", Predicate: `{"kind":"never"}`},
		},
	}
	sink := &InMemorySink{}
	exec, _ := NewExecutor(ExecConfig{
		RunID: "r-never", DAG: dag, Sink: sink,
		Registry: newBranchTestRegistry(),
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Nodes["downstream"].Status != NodeStatusSkipped {
		t.Fatalf("downstream should be skipped: %q", state.Nodes["downstream"].Status)
	}
	if state.Status != RunStatusCompleted {
		t.Fatalf("run should complete (skipped is not abort): %q", state.Status)
	}
}

func TestExecutorMalformedPredicateAborts(t *testing.T) {
	t.Parallel()

	dag := DAG{
		Nodes: []Node{
			{ID: "root", Type: "noop"},
			{ID: "down", Type: "noop"},
		},
		Edges: []Edge{
			{From: "root", To: "down", Predicate: `not json`},
		},
	}
	sink := &InMemorySink{}
	exec, _ := NewExecutor(ExecConfig{
		RunID: "r-bad-pred", DAG: dag, Sink: sink,
		Registry: newBranchTestRegistry(),
		Now:      fakeNow(),
		Sleep:    func(time.Duration) {},
	})
	state, err := exec.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.Status != RunStatusAborted {
		t.Fatalf("expected aborted run on malformed predicate, got %q", state.Status)
	}
}

func newDecoderRegistry() decoderRegistry {
	dr := decoderRegistry{m: map[string]func([]byte) (any, error){}}
	dr.m[EventTypeRunStarted] = func(b []byte) (any, error) { return decodeAs[RunStartedEvent](1, b) }
	dr.m[EventTypeRunCompleted] = func(b []byte) (any, error) { return decodeAs[RunCompletedEvent](1, b) }
	dr.m[EventTypeRunCancelled] = func(b []byte) (any, error) { return decodeAs[RunCancelledEvent](1, b) }
	dr.m[EventTypeRunAborted] = func(b []byte) (any, error) { return decodeAs[RunAbortedEvent](1, b) }
	dr.m[EventTypeNodeStarted] = func(b []byte) (any, error) { return decodeAs[NodeStartedEvent](1, b) }
	dr.m[EventTypeNodeSucceeded] = func(b []byte) (any, error) { return decodeAs[NodeSucceededEvent](1, b) }
	dr.m[EventTypeNodeFailed] = func(b []byte) (any, error) { return decodeAs[NodeFailedEvent](1, b) }
	dr.m[EventTypeNodeRetried] = func(b []byte) (any, error) { return decodeAs[NodeRetriedEvent](1, b) }
	dr.m[EventTypeNodeSkipped] = func(b []byte) (any, error) { return decodeAs[NodeSkippedEvent](1, b) }
	dr.m[EventTypePermissionRequested] = func(b []byte) (any, error) { return decodeAs[PermissionRequestedEvent](1, b) }
	dr.m[EventTypePermissionGranted] = func(b []byte) (any, error) { return decodeAs[PermissionGrantedEvent](1, b) }
	dr.m[EventTypePermissionDenied] = func(b []byte) (any, error) { return decodeAs[PermissionDeniedEvent](1, b) }
	return dr
}

type decoderRegistry struct {
	m map[string]func([]byte) (any, error)
}

func decodeAll(t *testing.T, dr decoderRegistry, events []SinkEntry) []any {
	t.Helper()
	out := make([]any, 0, len(events))
	for _, e := range events {
		fn, ok := dr.m[e.Type]
		if !ok {
			t.Fatalf("decoder missing for %q", e.Type)
		}
		v, err := fn(e.Payload)
		if err != nil {
			t.Fatalf("decode %q: %v", e.Type, err)
		}
		out = append(out, v)
	}
	return out
}
