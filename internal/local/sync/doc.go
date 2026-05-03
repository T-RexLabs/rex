// Package sync is the local-side sync client (sync.CLIENT.*).
//
// One Client speaks to one central node by URL. The package owns:
//
//   - HTTP wire (State, Push, Pull) over the sync.API surface.
//   - Per-remote watermark files at .rex/drafts/<remote>.toml that
//     record what the remote has acknowledged (storage.WS.2.12,
//     sync.DRAFT.1).
//   - Push/Pull/Sync flows that combine the wire calls with the
//     local events.log and watermark.
//
// The package is intentionally free of cobra; cli/run-style leaf
// commands wrap it. That keeps the round-trip testable via a real
// httptest server without dragging the CLI through.
package sync
