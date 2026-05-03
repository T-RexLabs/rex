// Package remotes is the local-side registry of named central nodes
// the user has attached to (sync.BOOT.1, storage.GLOBAL.5).
//
// The registry is a TOML file at ~/.config/rex/remotes.toml (or the
// platform equivalent). Each [<name>] section holds:
//
//   - url: the central node's base URL.
//   - fingerprint: the central's public-key fingerprint, recorded
//     after the first successful contact (TOFU per sync.BOOT.1.1);
//     left empty until then.
//   - added_at: when the local node first registered the remote.
//   - last_seen: when the local node last reached the remote
//     successfully.
//
// The registry is local-only and never syncs. Workspace-level remote
// references (.rex/remotes.toml from storage.WS.2.7) are a separate
// concern handled elsewhere.
package remotes
