// Package search is the workspace's local FTS5 index over events
// and specs (storage.INDEX, search.SCHEMA, search.QUERY).
//
// The index is a derived artifact (`storage.WS.2.11`,
// `search.scope-cut`/`PIPE`) — it never syncs and is rebuildable
// from `events.log` plus `.rex/specs/`. The on-disk file is
// `.rex/index.sqlite` opened via modernc.org/sqlite (pure Go, no
// cgo per overview.ENG.2).
//
// V1 indexes two entity types:
//
//   - events: every record from events.log, full-text searchable
//     by type + payload JSON.
//   - specs: every YAML spec under .rex/specs/, full-text searchable
//     by name + description + raw text.
//
// Other entity types from search.SCHEMA.1 (transcripts, file_changes,
// comments, schedules, audit_entries, workspace_metadata, repo_files)
// land alongside their producers when those producers exist.
//
// FTS5 tables run in standalone mode (no external-content with
// triggers). Simpler for v1; we trade a small amount of duplication
// for fewer moving parts.
package search
