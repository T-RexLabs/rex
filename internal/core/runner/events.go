package runner

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/asabla/rex/internal/core/event"
)

// Event type names. These are the strings that land in
// eventlog.Record.Type and the keys callers register decoders against.
// They match execution.DAG.2 verbatim.
const (
	EventTypeRunStarted    = "run.started"
	EventTypeRunCompleted  = "run.completed"
	EventTypeRunCancelled  = "run.cancelled"
	EventTypeRunAborted    = "run.aborted"
	EventTypeNodeStarted   = "node.started"
	EventTypeNodeSucceeded = "node.succeeded"
	EventTypeNodeFailed    = "node.failed"
	EventTypeNodeRetried   = "node.retried"
	// EventTypeNodeSkipped fires when an incoming edge's predicate
	// rejected the node (execution.PRIM.5). The node never runs;
	// downstream nodes whose only path was through this skip see
	// dependenciesMet return false and stay pending unless reached
	// from another path.
	EventTypeNodeSkipped         = "node.skipped"
	EventTypePermissionRequested = "permission.requested"
	EventTypePermissionGranted   = "permission.granted"
	EventTypePermissionDenied    = "permission.denied"
	// EventTypeHarnessFrame captures one ACP frame received from the
	// harness during a harness_invocation node. The payload mirrors
	// the wire shape (method + params for notifications, id + result
	// for responses); the executor and primitives stay agnostic to
	// what the harness actually said. (execution.ACP.3)
	EventTypeHarnessFrame = "harness.frame"

	// EventTypeRunCancellationRequested is emitted by `rex run cancel`
	// from a separate process and observed by the running process's
	// cancel watcher (cli.RUN.5 / execution.RUN.5). The watcher
	// translates it into ctx cancellation; the executor then emits
	// the canonical run.cancelled event when it returns.
	EventTypeRunCancellationRequested = "run.cancellation_requested"
)

// EventVersion is the schema version the runner currently emits. Bump
// only on a semantically incompatible change to an existing event
// shape; new fields must be additive (overview.SYS.4).
const EventVersion uint32 = 1

// RunStartedEvent fires when the Executor begins a Run.
//
// SpecRefs and FromTask are optional provenance fields recorded once
// at run start (execution.RUN.1.1). Trigger is a third optional
// provenance field recording the originating trigger when the run
// was started by the schedule daemon (execution.RUN.1.3 / SCHED.3).
// WorkType is the work-type tag from workspace.WORK.2 — present on
// every run, defaulting to "non_spec" when the caller doesn't
// specify; recorded once at run start so RBAC + indexing have
// something to filter on. All optional fields are additive per
// overview.SYS.4; readers skip unknown fields per overview.SYS.3.
type RunStartedEvent struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	// SpecRefs holds fully-qualified ACIDs the run is launched against.
	// Sourced from --spec-ref flags, the recipe's enclosing task
	// references, or both, deduplicated.
	SpecRefs []string `json:"spec_refs,omitempty"`
	// FromTask is the fully-qualified task reference of the form
	// `<spec-id>.<task-id>` when launched via --from-task or the
	// web-UI "Run this task" affordance. Empty for ad-hoc runs.
	FromTask string `json:"from_task,omitempty"`
	// Trigger records the originating trigger when the run was
	// started by the schedule daemon (execution.RUN.1.3). Nil for
	// ad-hoc runs. Unknown trigger kinds are tolerated by readers
	// (overview.SYS.3) so post-v1 trigger types load cleanly.
	Trigger *RunTrigger `json:"trigger,omitempty"`
	// WorkType is one of the five workspace.WORK.2 tags:
	// "question", "non_spec", "spec", "management", "scheduled".
	// Empty in events written before this field landed; readers
	// should treat empty as "non_spec" for back-compat.
	WorkType string `json:"work_type,omitempty"`
}

// Work-type tags per workspace.WORK.2.
const (
	WorkTypeQuestion   = "question"
	WorkTypeNonSpec    = "non_spec"
	WorkTypeSpec       = "spec"
	WorkTypeManagement = "management"
	WorkTypeScheduled  = "scheduled"
)

// IsValidWorkType reports whether s is one of the five recognised
// work-type tags. Used by the CLI to validate --work-type.
func IsValidWorkType(s string) bool {
	switch s {
	case WorkTypeQuestion, WorkTypeNonSpec, WorkTypeSpec,
		WorkTypeManagement, WorkTypeScheduled:
		return true
	}
	return false
}

// RunTrigger records the schedule that initiated a run. Field set
// is intentionally small and additive; "webhook" is reserved for v1.5.
type RunTrigger struct {
	// Kind is one of "cron", "file_watch". Reserved: "webhook".
	Kind string `json:"kind"`
	// Schedule is the .rex/schedules/<basename>.yaml that fired,
	// without extension.
	Schedule string `json:"schedule"`
	// Reason is a free-form human-readable explanation: the cron
	// expression that matched, the file paths that changed, etc.
	Reason string `json:"reason,omitempty"`
}

// RunCompletedEvent fires once every reachable Node has succeeded.
type RunCompletedEvent struct {
	RunID       string    `json:"run_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// RunCancelledEvent fires when the Executor honours a cancellation
// request from the caller (typically `rex run cancel`).
type RunCancelledEvent struct {
	RunID       string    `json:"run_id"`
	CancelledAt time.Time `json:"cancelled_at"`
	Reason      string    `json:"reason,omitempty"`
}

// RunCancellationRequestedEvent is emitted by `rex run cancel` from
// a separate process. The running process's cancel watcher
// translates an observed event into ctx cancellation; the
// canonical RunCancelledEvent is then emitted by the executor when
// it returns. Empty Requester is allowed but discouraged.
type RunCancellationRequestedEvent struct {
	RunID       string    `json:"run_id"`
	RequestedAt time.Time `json:"requested_at"`
	Requester   string    `json:"requester,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// RunAbortedEvent fires when the engine cannot continue — a Node
// exhausted retries, a primitive returned a non-retriable error, or an
// engine-internal error tripped.
type RunAbortedEvent struct {
	RunID     string    `json:"run_id"`
	AbortedAt time.Time `json:"aborted_at"`
	NodeID    NodeID    `json:"node_id,omitempty"`
	Reason    string    `json:"reason"`
}

// NodeStartedEvent fires on every attempt of a Node, including retries
// (a retried Node emits NodeRetried first then NodeStarted again).
type NodeStartedEvent struct {
	RunID     string    `json:"run_id"`
	NodeID    NodeID    `json:"node_id"`
	Attempt   int       `json:"attempt"`
	StartedAt time.Time `json:"started_at"`
}

// NodeSucceededEvent fires when a Node's primitive returns nil error.
// Output is the primitive's structured output, stored verbatim so
// downstream Nodes (and human watchers) can read it.
type NodeSucceededEvent struct {
	RunID       string          `json:"run_id"`
	NodeID      NodeID          `json:"node_id"`
	Attempt     int             `json:"attempt"`
	CompletedAt time.Time       `json:"completed_at"`
	Output      json.RawMessage `json:"output,omitempty"`
}

// NodeFailedEvent fires when a Node's primitive returns a non-nil
// error. The Executor decides whether to retry based on Attempt vs the
// effective RetryPolicy; this event is emitted regardless.
type NodeFailedEvent struct {
	RunID    string    `json:"run_id"`
	NodeID   NodeID    `json:"node_id"`
	Attempt  int       `json:"attempt"`
	FailedAt time.Time `json:"failed_at"`
	Error    string    `json:"error"`
}

// NodeRetriedEvent fires immediately before a retry attempt. It is
// distinct from NodeStarted so a watcher can show "retrying in 2s" UX.
type NodeRetriedEvent struct {
	RunID       string        `json:"run_id"`
	NodeID      NodeID        `json:"node_id"`
	NextAttempt int           `json:"next_attempt"`
	BackoffFor  time.Duration `json:"backoff_for"`
}

// NodeSkippedEvent fires when an incoming edge's predicate rejected
// the node (execution.PRIM.5). The "from" + "predicate" fields
// record which upstream / which test produced the skip so a
// reader can reconstruct the branching decision.
type NodeSkippedEvent struct {
	RunID     string    `json:"run_id"`
	NodeID    NodeID    `json:"node_id"`
	From      NodeID    `json:"from,omitempty"`
	Predicate string    `json:"predicate,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	SkippedAt time.Time `json:"skipped_at"`
}

// PermissionRequestedEvent fires when a primitive (typically
// harness_invocation receiving a session/request_permission) needs
// human authorization to proceed.
type PermissionRequestedEvent struct {
	RunID       string          `json:"run_id"`
	NodeID      NodeID          `json:"node_id"`
	RequestID   string          `json:"request_id"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	RequestedAt time.Time       `json:"requested_at"`
}

// PermissionGrantedEvent fires when a permission request is approved.
type PermissionGrantedEvent struct {
	RunID     string    `json:"run_id"`
	NodeID    NodeID    `json:"node_id"`
	RequestID string    `json:"request_id"`
	Approver  string    `json:"approver,omitempty"`
	GrantedAt time.Time `json:"granted_at"`
	Note      string    `json:"note,omitempty"`
}

// PermissionDeniedEvent fires when a permission request is denied.
type PermissionDeniedEvent struct {
	RunID     string    `json:"run_id"`
	NodeID    NodeID    `json:"node_id"`
	RequestID string    `json:"request_id"`
	Approver  string    `json:"approver,omitempty"`
	DeniedAt  time.Time `json:"denied_at"`
	Reason    string    `json:"reason,omitempty"`
}

// HarnessFrameEvent captures one ACP frame received from the harness
// during a harness_invocation node — the actual transcript content
// (model text chunks, tool calls, tool results, anything the bridge
// streams). Frame is the JSON of the inner ACP message; readers can
// further decode it by Method / inspect Result for typed views.
//
// We persist the wire payload verbatim rather than splitting it into
// "agent text" / "tool call" / etc. typed events so additive
// upstream evolution (overview.SYS.4) doesn't force a rex schema
// migration: as the ACP grows new update types, the frames flow
// through unchanged and renderers (cli watch / web run-detail)
// catch up at their own pace.
type HarnessFrameEvent struct {
	RunID     string          `json:"run_id"`
	NodeID    NodeID          `json:"node_id"`
	SessionID string          `json:"session_id,omitempty"`
	Method    string          `json:"method,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Frame     json.RawMessage `json:"frame"`
	At        time.Time       `json:"at"`
}

// RegisterEvents adds decoders for every runner event type to r so
// readers (replay, watchers) can decode runner events without each
// reader rebuilding its own table.
func RegisterEvents(r *event.Registry) {
	r.Register(EventTypeRunStarted, EventVersion, decodeAs[RunStartedEvent])
	r.Register(EventTypeRunCompleted, EventVersion, decodeAs[RunCompletedEvent])
	r.Register(EventTypeRunCancelled, EventVersion, decodeAs[RunCancelledEvent])
	r.Register(EventTypeRunAborted, EventVersion, decodeAs[RunAbortedEvent])
	r.Register(EventTypeNodeStarted, EventVersion, decodeAs[NodeStartedEvent])
	r.Register(EventTypeNodeSucceeded, EventVersion, decodeAs[NodeSucceededEvent])
	r.Register(EventTypeNodeFailed, EventVersion, decodeAs[NodeFailedEvent])
	r.Register(EventTypeNodeRetried, EventVersion, decodeAs[NodeRetriedEvent])
	r.Register(EventTypeNodeSkipped, EventVersion, decodeAs[NodeSkippedEvent])
	r.Register(EventTypePermissionRequested, EventVersion, decodeAs[PermissionRequestedEvent])
	r.Register(EventTypePermissionGranted, EventVersion, decodeAs[PermissionGrantedEvent])
	r.Register(EventTypePermissionDenied, EventVersion, decodeAs[PermissionDeniedEvent])
	r.Register(EventTypeHarnessFrame, EventVersion, decodeAs[HarnessFrameEvent])
	r.Register(EventTypeRunCancellationRequested, EventVersion, decodeAs[RunCancellationRequestedEvent])
}

func decodeAs[T any](_ uint32, payload []byte) (any, error) {
	var v T
	if err := json.Unmarshal(payload, &v); err != nil {
		return nil, fmt.Errorf("runner: decode %T: %w", v, err)
	}
	return v, nil
}

// classifyEvent returns the (type, version) Rex should stamp on the
// envelope when this runner event lands in events.log. Returns an
// error for an unrecognized event value.
func classifyEvent(evt any) (string, uint32, error) {
	switch evt.(type) {
	case RunStartedEvent:
		return EventTypeRunStarted, EventVersion, nil
	case RunCompletedEvent:
		return EventTypeRunCompleted, EventVersion, nil
	case RunCancelledEvent:
		return EventTypeRunCancelled, EventVersion, nil
	case RunAbortedEvent:
		return EventTypeRunAborted, EventVersion, nil
	case NodeStartedEvent:
		return EventTypeNodeStarted, EventVersion, nil
	case NodeSucceededEvent:
		return EventTypeNodeSucceeded, EventVersion, nil
	case NodeFailedEvent:
		return EventTypeNodeFailed, EventVersion, nil
	case NodeRetriedEvent:
		return EventTypeNodeRetried, EventVersion, nil
	case NodeSkippedEvent:
		return EventTypeNodeSkipped, EventVersion, nil
	case PermissionRequestedEvent:
		return EventTypePermissionRequested, EventVersion, nil
	case PermissionGrantedEvent:
		return EventTypePermissionGranted, EventVersion, nil
	case PermissionDeniedEvent:
		return EventTypePermissionDenied, EventVersion, nil
	case HarnessFrameEvent:
		return EventTypeHarnessFrame, EventVersion, nil
	case RunCancellationRequestedEvent:
		return EventTypeRunCancellationRequested, EventVersion, nil
	}
	return "", 0, fmt.Errorf("runner: unknown event type %T", evt)
}
