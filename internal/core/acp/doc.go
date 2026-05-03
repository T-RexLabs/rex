// Package acp implements the Agent Client Protocol client side that Rex
// uses to drive harnesses (Claude Code, Codex, OpenCode, ...).
//
// The contract comes from execution.ACP. ACP is JSON-RPC 2.0 framed as
// newline-delimited JSON over stdio (default) or a local socket. This
// package owns the wire layer and the session lifecycle; the executor
// glues it to the run DAG.
//
// One non-obvious requirement: every received frame must be captured to
// the run's transcript before any further processing (execution.ACP.3).
// To make that easy, the framing layer hands callers both the parsed
// Message and the raw bytes it was decoded from, so capture is a single
// append to the event log without re-marshaling.
package acp
