package web

// WorkspaceIndexRow is one row on the per-org workspace index
// page (/orgs/<org-id>/workspaces). Lifted to internalweb so the
// shared template renders against the same struct regardless of
// where the resolver gets the list from.
//
// v1 single-workspace limitation: the central side currently
// reports the one workspace bound to its GitStore for every
// orgID; multi-workspace dispatch lands with the GitStore
// scoping refactor.
type WorkspaceIndexRow struct {
	ID    string
	Name  string
	State string
}

// WorkspacesIndexProjection is the read-side surface the shared
// /orgs/<org-id>/workspaces handler queries. Implementations
// dispatch on orgID; v1 central impls ignore it (single-workspace
// GitStore limitation) and return the one workspace they hold.
type WorkspacesIndexProjection interface {
	ListWorkspaces(orgID string) ([]WorkspaceIndexRow, error)
}
