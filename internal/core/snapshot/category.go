package snapshot

import "github.com/asabla/rex/internal/core/storage/synccat"

// SyncCategory is the sync category snapshots belong to. Snapshots are
// local-only derived state per storage.SNAP.2 / sync.CAT.4 — they are
// rebuilt independently on each node and never replicate.
const SyncCategory = synccat.CategoryDerived
