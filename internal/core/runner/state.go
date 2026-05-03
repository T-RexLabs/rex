package runner

import (
	"encoding/json"
	"fmt"
	"time"
)

// NodeState is the per-Node slice of RunState.
type NodeState struct {
	Status      NodeStatus      `json:"status"`
	Attempts    int             `json:"attempts"`
	LastError   string          `json:"last_error,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	StartedAt   time.Time       `json:"started_at,omitempty"`
	CompletedAt time.Time       `json:"completed_at,omitempty"`
}

// PermissionState captures a permission request and its eventual
// resolution. Stored on RunState so a watcher arriving mid-run sees
// pending approvals without re-reading the event log.
type PermissionState struct {
	NodeID     NodeID
	Tool       string
	Reason     string
	RequestedAt time.Time
	Resolved   bool
	Granted    bool
	Approver   string
	Note       string
}

// RunState is the engine state derived from the event log. Two RunStates
// produced by Replay over the same event sequence (in order) are
// guaranteed identical — execution.DAG.3 is a structural property of
// this fold.
type RunState struct {
	RunID       string
	DAG         DAG
	Status      RunStatus
	StartedAt   time.Time
	CompletedAt time.Time
	AbortedAt   time.Time
	AbortReason string
	Nodes       map[NodeID]*NodeState
	Permissions map[string]*PermissionState
}

// NewState returns a RunState seeded with every Node in pending status
// — the shape Replay starts from.
func NewState(dag DAG) *RunState {
	s := &RunState{
		DAG:         dag,
		Status:      RunStatusPending,
		Nodes:       make(map[NodeID]*NodeState, len(dag.Nodes)),
		Permissions: make(map[string]*PermissionState),
	}
	for _, n := range dag.Nodes {
		s.Nodes[n.ID] = &NodeState{Status: NodeStatusPending}
	}
	return s
}

// Apply folds one decoded runner event into s. It returns an error if
// evt is not a recognized runner event type — Replay treats that as a
// hard failure rather than skipping silently, since unknown event
// payloads on a known event type indicate an upgrade was missed
// (storage.EVENTS.5 already covers the unknown-type case at decode
// time).
func (s *RunState) Apply(evt any) error {
	switch e := evt.(type) {
	case RunStartedEvent:
		s.RunID = e.RunID
		s.StartedAt = e.StartedAt
		s.Status = RunStatusRunning
	case RunCompletedEvent:
		s.CompletedAt = e.CompletedAt
		s.Status = RunStatusCompleted
	case RunCancelledEvent:
		s.CompletedAt = e.CancelledAt
		s.AbortReason = e.Reason
		s.Status = RunStatusCancelled
	case RunAbortedEvent:
		s.AbortedAt = e.AbortedAt
		s.AbortReason = e.Reason
		s.Status = RunStatusAborted
	case NodeStartedEvent:
		ns, err := s.nodeState(e.NodeID)
		if err != nil {
			return err
		}
		ns.Status = NodeStatusRunning
		ns.Attempts = e.Attempt
		ns.StartedAt = e.StartedAt
	case NodeSucceededEvent:
		ns, err := s.nodeState(e.NodeID)
		if err != nil {
			return err
		}
		ns.Status = NodeStatusSucceeded
		ns.CompletedAt = e.CompletedAt
		ns.Output = e.Output
		ns.LastError = ""
	case NodeFailedEvent:
		ns, err := s.nodeState(e.NodeID)
		if err != nil {
			return err
		}
		ns.Status = NodeStatusFailed
		ns.CompletedAt = e.FailedAt
		ns.LastError = e.Error
	case NodeRetriedEvent:
		ns, err := s.nodeState(e.NodeID)
		if err != nil {
			return err
		}
		// NodeStarted will overwrite once the retry begins; this
		// transient state lets a watcher say "queued for retry".
		ns.Status = NodeStatusPending
	case PermissionRequestedEvent:
		s.Permissions[e.RequestID] = &PermissionState{
			NodeID:      e.NodeID,
			Tool:        e.Tool,
			Reason:      e.Reason,
			RequestedAt: e.RequestedAt,
		}
	case PermissionGrantedEvent:
		ps, ok := s.Permissions[e.RequestID]
		if !ok {
			return fmt.Errorf("runner: granted unknown permission request %q", e.RequestID)
		}
		ps.Resolved = true
		ps.Granted = true
		ps.Approver = e.Approver
		ps.Note = e.Note
	case PermissionDeniedEvent:
		ps, ok := s.Permissions[e.RequestID]
		if !ok {
			return fmt.Errorf("runner: denied unknown permission request %q", e.RequestID)
		}
		ps.Resolved = true
		ps.Granted = false
		ps.Approver = e.Approver
		ps.Note = e.Reason
	default:
		return fmt.Errorf("runner: state.Apply unknown event %T", evt)
	}
	return nil
}

func (s *RunState) nodeState(id NodeID) (*NodeState, error) {
	ns, ok := s.Nodes[id]
	if !ok {
		return nil, fmt.Errorf("runner: event references unknown node %q", id)
	}
	return ns, nil
}

// Replay reconstructs a RunState from a sequence of decoded runner
// events. The events must be in the order they were appended — the
// causal/HLC ordering established by storage.EVENTS.3.
func Replay(dag DAG, events []any) (*RunState, error) {
	s := NewState(dag)
	for i, evt := range events {
		if err := s.Apply(evt); err != nil {
			return nil, fmt.Errorf("runner: replay event %d: %w", i, err)
		}
	}
	return s, nil
}
