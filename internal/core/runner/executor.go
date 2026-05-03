package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EventSink receives runner events. The package ships
// internal/core/storage/eventlog as one implementation; tests use
// InMemorySink.
type EventSink interface {
	// Append persists the event. The sink is responsible for stamping
	// HLC, workspace, actor — the runner only knows the event type,
	// version, and payload.
	Append(eventType string, version uint32, payload json.RawMessage) error
}

// ExecConfig configures one Run.
type ExecConfig struct {
	RunID    string
	DAG      DAG
	Sink     EventSink
	Registry *PrimitiveRegistry

	// Now and Sleep are injectable so tests can drive retry timing
	// without burning real time (overview.ENG.4). When nil, time.Now
	// and time.Sleep are used.
	Now   func() time.Time
	Sleep func(time.Duration)
}

// Executor runs one DAG to completion, emitting events as it goes.
type Executor struct {
	cfg ExecConfig
}

// NewExecutor validates the configuration and returns a ready Executor.
func NewExecutor(cfg ExecConfig) (*Executor, error) {
	if cfg.RunID == "" {
		return nil, errors.New("runner: ExecConfig.RunID is required")
	}
	if cfg.Sink == nil {
		return nil, errors.New("runner: ExecConfig.Sink is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("runner: ExecConfig.Registry is required")
	}
	if err := cfg.DAG.Validate(); err != nil {
		return nil, err
	}
	if err := validateRegistered(cfg.DAG, cfg.Registry); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Executor{cfg: cfg}, nil
}

// Run executes the DAG. The returned RunState reflects the engine's
// view at the moment Run returned — the same RunState Replay would
// produce from the events Run emitted, modulo never-applied event
// rejections (which by construction don't happen).
//
// Run respects context cancellation: if ctx is cancelled mid-run, the
// engine emits RunCancelledEvent with the cause as the reason and
// returns. Run does not return ctx.Err() — cancellation is part of the
// run lifecycle, not a runtime failure.
func (e *Executor) Run(ctx context.Context) (*RunState, error) {
	state := NewState(e.cfg.DAG)
	state.RunID = e.cfg.RunID
	startedAt := e.cfg.Now()

	if err := e.emitApply(state, RunStartedEvent{
		RunID:     e.cfg.RunID,
		StartedAt: startedAt,
	}); err != nil {
		return state, err
	}

	queue := append([]NodeID(nil), e.cfg.DAG.roots()...)

	for len(queue) > 0 {
		if cancelled := e.checkCancel(state, ctx); cancelled {
			return state, nil
		}

		nodeID := queue[0]
		queue = queue[1:]

		node, ok := findNode(e.cfg.DAG, nodeID)
		if !ok {
			// Unreachable given DAG.Validate, but explicit beats panic.
			_ = e.emitApply(state, RunAbortedEvent{
				RunID:     e.cfg.RunID,
				AbortedAt: e.cfg.Now(),
				NodeID:    nodeID,
				Reason:    fmt.Sprintf("internal: missing node %q", nodeID),
			})
			return state, nil
		}

		outcome, abortReason := e.runNode(ctx, state, node)
		switch outcome {
		case nodeOutcomeCancelled:
			// runNode already emitted RunCancelledEvent.
			return state, nil
		case nodeOutcomeAborted:
			_ = e.emitApply(state, RunAbortedEvent{
				RunID:     e.cfg.RunID,
				AbortedAt: e.cfg.Now(),
				NodeID:    nodeID,
				Reason:    abortReason,
			})
			return state, nil
		}

		// Schedule downstream Nodes whose dependencies are now all met.
		for _, edge := range e.cfg.DAG.Edges {
			if edge.From != nodeID {
				continue
			}
			if dependenciesMet(e.cfg.DAG, state, edge.To) {
				queue = append(queue, edge.To)
			}
		}
	}

	if err := e.emitApply(state, RunCompletedEvent{
		RunID:       e.cfg.RunID,
		CompletedAt: e.cfg.Now(),
	}); err != nil {
		return state, err
	}
	return state, nil
}

// nodeOutcome enumerates how runNode terminated.
type nodeOutcome int

const (
	nodeOutcomeSucceeded nodeOutcome = iota
	nodeOutcomeAborted
	nodeOutcomeCancelled
)

// runNode executes one Node with retry. The returned reason is set
// only when the outcome is nodeOutcomeAborted; for nodeOutcomeCancelled
// runNode has already emitted RunCancelledEvent.
func (e *Executor) runNode(ctx context.Context, state *RunState, node Node) (nodeOutcome, string) {
	prim, _ := e.cfg.Registry.Get(node.Type) // existence already validated
	policy := node.Retry.Effective(e.cfg.DAG.DefaultRetry)

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if cancelled := e.checkCancel(state, ctx); cancelled {
			return nodeOutcomeCancelled, ""
		}

		if err := e.emitApply(state, NodeStartedEvent{
			RunID:     e.cfg.RunID,
			NodeID:    node.ID,
			Attempt:   attempt,
			StartedAt: e.cfg.Now(),
		}); err != nil {
			return nodeOutcomeAborted, fmt.Sprintf("emit NodeStarted: %v", err)
		}

		out, runErr := prim.Run(ctx, PrimitiveInput{
			RunID: e.cfg.RunID,
			Node:  node,
			State: state,
		})

		// A context-cancelled return is a run-cancellation, not a
		// node failure — even if the primitive surfaced ctx.Err()
		// directly. Doing this check after Run keeps primitives
		// simple: they can return ctx.Err() naively and still get
		// correct lifecycle bookkeeping.
		if cancelled := e.checkCancel(state, ctx); cancelled {
			return nodeOutcomeCancelled, ""
		}

		if runErr == nil {
			if err := e.emitApply(state, NodeSucceededEvent{
				RunID:       e.cfg.RunID,
				NodeID:      node.ID,
				Attempt:     attempt,
				CompletedAt: e.cfg.Now(),
				Output:      out.Output,
			}); err != nil {
				return nodeOutcomeAborted, fmt.Sprintf("emit NodeSucceeded: %v", err)
			}
			return nodeOutcomeSucceeded, ""
		}

		if err := e.emitApply(state, NodeFailedEvent{
			RunID:    e.cfg.RunID,
			NodeID:   node.ID,
			Attempt:  attempt,
			FailedAt: e.cfg.Now(),
			Error:    runErr.Error(),
		}); err != nil {
			return nodeOutcomeAborted, fmt.Sprintf("emit NodeFailed: %v", err)
		}

		if attempt == policy.MaxAttempts {
			return nodeOutcomeAborted, fmt.Sprintf("node %q exhausted %d attempts: %v", node.ID, policy.MaxAttempts, runErr)
		}

		backoff := time.Duration(attempt) * policy.Backoff
		if err := e.emitApply(state, NodeRetriedEvent{
			RunID:       e.cfg.RunID,
			NodeID:      node.ID,
			NextAttempt: attempt + 1,
			BackoffFor:  backoff,
		}); err != nil {
			return nodeOutcomeAborted, fmt.Sprintf("emit NodeRetried: %v", err)
		}
		e.cfg.Sleep(backoff)
	}

	// Unreachable in practice — the loop always returns inside its
	// success/exhaustion branches — but keeps the compiler happy.
	return nodeOutcomeAborted, "internal: retry loop fell through"
}

// checkCancel emits RunCancelledEvent if ctx is done. Returns true when
// the caller should stop and propagate cancellation upward.
func (e *Executor) checkCancel(state *RunState, ctx context.Context) bool {
	err := ctx.Err()
	if err == nil {
		return false
	}
	_ = e.emitApply(state, RunCancelledEvent{
		RunID:       e.cfg.RunID,
		CancelledAt: e.cfg.Now(),
		Reason:      err.Error(),
	})
	return true
}

// emitApply marshals evt, hands it to the sink, and updates state. A
// sink failure is propagated; an Apply failure is also propagated since
// it indicates a bug in the runner (the runner emits only events it
// can apply).
func (e *Executor) emitApply(state *RunState, evt any) error {
	typ, ver, err := classifyEvent(evt)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("runner: marshal %T: %w", evt, err)
	}
	if err := e.cfg.Sink.Append(typ, ver, payload); err != nil {
		return fmt.Errorf("runner: sink append: %w", err)
	}
	if err := state.Apply(evt); err != nil {
		return fmt.Errorf("runner: apply emitted event: %w", err)
	}
	return nil
}

func findNode(dag DAG, id NodeID) (Node, bool) {
	for _, n := range dag.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// dependenciesMet reports whether every in-edge to id has a Succeeded
// upstream node in state.
func dependenciesMet(dag DAG, state *RunState, id NodeID) bool {
	met := false
	for _, e := range dag.Edges {
		if e.To != id {
			continue
		}
		met = true
		ns, ok := state.Nodes[e.From]
		if !ok || ns.Status != NodeStatusSucceeded {
			return false
		}
	}
	// A node with no in-edges is a root and would already be queued at
	// start; here we only consider nodes that *do* have edges.
	return met
}

// InMemorySink is a test-friendly EventSink that buffers events.
type InMemorySink struct {
	Events []SinkEntry
}

// SinkEntry is one captured event.
type SinkEntry struct {
	Type    string
	Version uint32
	Payload json.RawMessage
}

// Append records the event verbatim.
func (s *InMemorySink) Append(eventType string, version uint32, payload json.RawMessage) error {
	s.Events = append(s.Events, SinkEntry{
		Type:    eventType,
		Version: version,
		Payload: append(json.RawMessage(nil), payload...),
	})
	return nil
}

// Types returns just the type strings of every captured event, in
// order. Lets tests assert on ordering succinctly.
func (s *InMemorySink) Types() []string {
	out := make([]string, len(s.Events))
	for i, e := range s.Events {
		out[i] = e.Type
	}
	return out
}
