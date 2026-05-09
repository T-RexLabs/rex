package specfmt

import "github.com/asabla/rex/internal/core/storage/synccat"

// SyncCategory is the sync category spec YAML files belong to. Specs
// are human-authored content under `.rex/specs/` and sync via
// three-way merge with auto-rebase per sync.CAT.2.
const SyncCategory = synccat.CategoryGitMerged
