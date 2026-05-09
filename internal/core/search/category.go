package search

import "github.com/asabla/rex/internal/core/storage/synccat"

// SyncCategory is the sync category the SQLite FTS5 index belongs to.
// The index is rebuilt deterministically from events.log + git-merged
// content per storage.INDEX.1-3 and never transmits — sync.CAT.4.
const SyncCategory = synccat.CategoryDerived
