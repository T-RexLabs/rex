// Package runner is the embedded event-sourced workflow engine
// described in execution.DAG and execution.PRIM.
//
// A Run is a DAG of primitive Nodes. The Executor walks the DAG, calls
// each Node's Primitive, and emits an event for every state transition
// (run.started, node.started, node.succeeded, node.failed, node.retried,
// permission.requested/granted/denied, run.completed/cancelled/aborted).
// Replaying the event sequence with Replay reconstructs the exact
// RunState — that's the contract execution.DAG.3 promises and the
// foundation for `rex run watch --since-start` and crash recovery.
//
// Runner is intentionally not aware of storage; it talks to an EventSink
// and lets the caller decide how events are persisted. For Rex that's
// the events.log writer in internal/core/storage/eventlog, but tests
// can use an in-memory sink.
package runner
