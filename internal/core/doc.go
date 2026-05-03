// Package core holds logic shared by both the local node (cmd/rex) and
// the central node (cmd/rex-central).
//
// Per overview.SYS.1, Rex builds local and central from a single Go module
// and keeps any per-flavor difference inside the cmd/* binaries (the thin
// shell) or behind build tags. No core package may branch on whether it is
// running on a local or central node.
//
// Subpackages add capability vertically (event envelope, storage, sync,
// execution, ...). Each subpackage owns its sync category per
// overview.SYS.2 and is the source of truth for the contract its callers
// rely on.
package core
