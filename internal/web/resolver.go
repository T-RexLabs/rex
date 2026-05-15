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

// Workspace is the resolver's view of one workspace. Root is set
// for file-backed workspaces (the local shell); central-shell
// resolvers leave Root empty and surface content via projection
// methods added in later cuts.
type Workspace struct {
	// ID is the workspace's canonical id from workspace.yaml.
	ID string
	// Root is the absolute filesystem path to the workspace
	// directory. Empty for projection-backed workspaces (central).
	Root string
}
