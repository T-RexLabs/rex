// Package hooks dispatches workspace events to user-installed hook
// scripts (specs/hooks.yaml).
//
// Per hooks.LOC, hooks live in two locations: per-workspace under
// .rex/hooks/<event-name> (git-merged with the workspace) and
// global under <user-config-dir>/rex/hooks/<event-name> (per-machine).
// Per-workspace hooks fire first, then global.
//
// The Dispatcher is non-blocking: callers hand off an event via
// OnAppend (matching the eventlog.SignFunc-shaped callback shape)
// and the dispatcher enqueues work to a bounded worker pool. A slow
// hook cannot block the event-producing operation per hooks.EXEC.2.
//
// Output handling: each hook's stdout and stderr are captured to a
// log file at .rex/hook-log/<event-id>.<hook-name>.log
// (hooks.EXEC.4). Non-zero exit codes do not fail the dispatcher
// itself; they are visible only via the log file. Per-hook timeout
// defaults to 30 seconds; on timeout the dispatcher SIGTERMs then
// SIGKILLs the subprocess (hooks.EXEC.3).
package hooks
