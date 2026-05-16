package web

// WorkspaceResolver maps a workspace id to its content. The local
// shell's resolver is single-workspace-bound (ignores the input and
// always returns its bound workspace); the central shell's resolver
// (lands with central-read-side-pages) looks the id up against
// Postgres-backed projections (web-ui.CENTRAL-LAYOUT.2).
//
// The interface is intentionally minimal in this initial cut.
// Read-side handlers that move into this package as part of
// central-read-side-pages will widen Workspace with the projection
// methods they actually need; the additive shape lets the local
// shell extend its resolver without breaking older central code and
// vice versa.
type WorkspaceResolver interface {
	// Resolve returns the workspace identified by workspaceID.
	// Single-workspace-bound resolvers ignore workspaceID and always
	// return their bound workspace; multi-workspace resolvers look
	// it up. Returns a non-nil error when the workspace is unknown
	// or unreachable.
	Resolve(workspaceID string) (Workspace, error)
}

// ScopeOption is one entry in the topbar scope picker partial.
type ScopeOption struct {
	Value string
	Label string
}

// ScopePickerData is the partial-friendly shape for the
// scope_picker template. Both shells construct one of these for
// their page envelope so the shared partial renders identically
// (web-ui.SHARED.2).
type ScopePickerData struct {
	Selected string
	Remotes  []ScopeOption
}

// Workspace is the resolver's view of one workspace. Local
// resolvers populate Root + projection fields with file-backed
// implementations; central resolvers leave Root empty and bind
// projection fields against the Postgres / GitStore-backed
// readers.
//
// Projection fields grow per lifted handler (web-ui.CENTRAL-LAYOUT.2,
// Decision A in the 2026-05-16 amendment). The local shell and
// central shell may each leave a projection nil for surfaces they
// don't yet serve — handlers should guard against nil to keep the
// shared handler set forward-compatible during the lift sequence.
type Workspace struct {
	// ID is the workspace's canonical id from workspace.yaml.
	ID string
	// Root is the absolute filesystem path to the workspace
	// directory. Empty for projection-backed workspaces (central).
	Root string
	// Specs serves the shared /specs and /specs/<id> handlers.
	// nil when the shell hasn't bound a spec projection (e.g. a
	// fresh deployment with no workspace.yaml yet).
	Specs SpecProjection
	// Runs serves the shared /runs list handler. nil leaves the
	// page empty (handlers guard).
	Runs RunsListProjection
	// RunDetail serves the shared /runs/<id> terminal-state
	// handler. Local shells leave this nil because they have a
	// rich local-only detail flow (frame view + permission UI +
	// SSE); central shells bind it for terminal-state rendering.
	RunDetail RunDetailProjection
	// Audit serves the shared /audit handler. nil leaves the
	// page empty.
	Audit AuditProjection
	// Amendments serves the shared /amendments index + detail
	// handlers. nil leaves the page empty.
	Amendments AmendmentsProjection
	// Search serves the shared /search handler. nil makes the
	// search page render with a "backend not yet wired" notice
	// instead of running queries (the central shell's v1 path
	// until Postgres FTS lands per central-node.DB.4).
	Search SearchProjection
	// Remotes serves the shared /remotes handler. Local
	// resolvers bind the per-machine registry; central resolvers
	// bind the workspace's synced `.rex/remotes.toml` from the
	// GitStore (read-only on central per amendment Decision C).
	Remotes RemotesProjection
}
