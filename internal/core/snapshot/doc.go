// Package snapshot implements the local-only point-in-time snapshot
// surface from storage.SNAP.
//
// A snapshot is a copy of a workspace's git-merged content
// (workspace.yaml, specs/, schedules/, templates/, hooks/) plus a
// manifest that records the snapshot id, creation time, and the
// HLC of the events.log head at snapshot time. Snapshots live at
// .rex/snapshots/<id>/ and are local-only — they never sync
// (storage.SNAP.2).
//
// V1 scope is intentionally narrow:
//
//   - Captures git-merged content only. The "index" component
//     listed in storage.SNAP.1 is deferred until index.sqlite
//     itself exists; the "summarized event-log state" reduces to
//     the manifest's last_event_id pointer.
//   - Manual triggers only. The auto-triggers in storage.SNAP.3
//     (every-N-events, every-T-duration) need a daemon model the
//     v1 CLI does not provide; they will land alongside that.
//   - Restore is a content rollback over the git-merged set;
//     events.log is left untouched per storage.SNAP.4.
//   - Prune is on-demand. Auto-prune from storage.SNAP.5 lands
//     with the daemon work.
package snapshot
