package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// NodeID identifies a Node within a DAG. NodeIDs are caller-chosen and
// must be unique inside one DAG; the executor does not generate them.
type NodeID string

// RetryPolicy controls how the executor retries a failed Primitive.
// MaxAttempts of 1 means "no retry"; 0 means "use the DAG default".
// Backoff is the linear delay added between successive attempts: the
// nth retry sleeps Backoff*(n-1) before re-running.
type RetryPolicy struct {
	MaxAttempts int           `json:"max_attempts,omitempty"`
	Backoff     time.Duration `json:"backoff,omitempty"`
}

// DefaultRetryPolicy is execution.DAG.4: up to 3 attempts with linear
// backoff (1s, 2s, 3s). Caller may override per-DAG or per-Node.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 3, Backoff: time.Second}

// Effective resolves Policy against the DAG default and the package
// default, in that order. Concrete fields on the more-specific policy
// win.
func (p RetryPolicy) Effective(dagDefault RetryPolicy) RetryPolicy {
	out := p
	if out.MaxAttempts == 0 {
		out.MaxAttempts = dagDefault.MaxAttempts
	}
	if out.Backoff == 0 {
		out.Backoff = dagDefault.Backoff
	}
	if out.MaxAttempts == 0 {
		out.MaxAttempts = DefaultRetryPolicy.MaxAttempts
	}
	if out.Backoff == 0 {
		out.Backoff = DefaultRetryPolicy.Backoff
	}
	return out
}

// Node is one unit of work in a DAG.
type Node struct {
	ID     NodeID          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
	// RequiresApproval gates this Node behind a human approval per
	// execution.PRIM.10. The skeleton emits permission events but the
	// full prompt-and-pause flow lives in the executor task.
	RequiresApproval bool        `json:"requires_approval,omitempty"`
	Retry            RetryPolicy `json:"retry,omitempty"`
}

// Edge is a directed connection between two Nodes. Predicate is left
// for execution.PRIM.5 (branch); a nil Predicate means the edge always
// fires once From has succeeded.
type Edge struct {
	From      NodeID `json:"from"`
	To        NodeID `json:"to"`
	Predicate string `json:"predicate,omitempty"` // unused in v1 skeleton
}

// DAG is the static description of a Run. It is immutable for the
// duration of a Run; mutations belong on the RunState produced by
// Replay or the live Executor.
type DAG struct {
	Nodes        []Node      `json:"nodes"`
	Edges        []Edge      `json:"edges"`
	DefaultRetry RetryPolicy `json:"default_retry,omitempty"`
}

// Validate checks structural invariants: unique node IDs, edges that
// reference declared nodes, and an acyclic shape. Returns a single
// joined error so callers can report all issues at once.
func (d DAG) Validate() error {
	var problems []error
	ids := make(map[NodeID]struct{}, len(d.Nodes))
	for _, n := range d.Nodes {
		if n.ID == "" {
			problems = append(problems, errors.New("runner: node has empty ID"))
			continue
		}
		if _, dup := ids[n.ID]; dup {
			problems = append(problems, fmt.Errorf("runner: duplicate node ID %q", n.ID))
			continue
		}
		ids[n.ID] = struct{}{}
		if n.Type == "" {
			problems = append(problems, fmt.Errorf("runner: node %q has empty type", n.ID))
		}
	}
	for _, e := range d.Edges {
		if _, ok := ids[e.From]; !ok {
			problems = append(problems, fmt.Errorf("runner: edge references unknown From node %q", e.From))
		}
		if _, ok := ids[e.To]; !ok {
			problems = append(problems, fmt.Errorf("runner: edge references unknown To node %q", e.To))
		}
	}
	if cycle := d.findCycle(); cycle != "" {
		problems = append(problems, fmt.Errorf("runner: cycle detected involving %s", cycle))
	}
	return errors.Join(problems...)
}

// findCycle returns a comma-separated list of node IDs that form a
// cycle, or "" if the DAG is acyclic. Implementation is a standard
// three-colour DFS.
func (d DAG) findCycle() string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	colour := make(map[NodeID]int, len(d.Nodes))
	for _, n := range d.Nodes {
		colour[n.ID] = white
	}

	adj := make(map[NodeID][]NodeID, len(d.Nodes))
	for _, e := range d.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	var path []NodeID
	var cycle []NodeID

	var visit func(NodeID) bool
	visit = func(id NodeID) bool {
		if colour[id] == gray {
			// Slice path back to the first occurrence of id.
			for i, p := range path {
				if p == id {
					cycle = append([]NodeID{}, path[i:]...)
					cycle = append(cycle, id)
					return true
				}
			}
		}
		if colour[id] == black {
			return false
		}
		colour[id] = gray
		path = append(path, id)
		for _, next := range adj[id] {
			if visit(next) {
				return true
			}
		}
		path = path[:len(path)-1]
		colour[id] = black
		return false
	}

	for _, n := range d.Nodes {
		if colour[n.ID] == white {
			if visit(n.ID) {
				break
			}
		}
	}

	if len(cycle) == 0 {
		return ""
	}
	out := ""
	for i, id := range cycle {
		if i > 0 {
			out += " -> "
		}
		out += string(id)
	}
	return out
}

// roots returns the NodeIDs that have no incoming edge — the entry
// points the executor schedules first.
func (d DAG) roots() []NodeID {
	hasIncoming := make(map[NodeID]bool, len(d.Nodes))
	for _, e := range d.Edges {
		hasIncoming[e.To] = true
	}
	var roots []NodeID
	for _, n := range d.Nodes {
		if !hasIncoming[n.ID] {
			roots = append(roots, n.ID)
		}
	}
	return roots
}

// NodeStatus is the lifecycle of one Node within a Run.
type NodeStatus string

const (
	NodeStatusPending   NodeStatus = "pending"
	NodeStatusRunning   NodeStatus = "running"
	NodeStatusSucceeded NodeStatus = "succeeded"
	NodeStatusFailed    NodeStatus = "failed"
	NodeStatusSkipped   NodeStatus = "skipped"
)

// RunStatus is the lifecycle of a Run as a whole. Note the absence of a
// "failed" terminal — execution.DAG.2 distinguishes:
//   - completed: every reachable node succeeded
//   - cancelled: user requested cancel
//   - aborted: a node exhausted retries or the engine itself failed
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusCancelled RunStatus = "cancelled"
	RunStatusAborted   RunStatus = "aborted"
)
