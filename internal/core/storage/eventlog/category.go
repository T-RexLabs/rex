package eventlog

import "github.com/asabla/rex/internal/core/storage/synccat"

// SyncCategory is the sync category the events.log file belongs to.
// Every record this package writes is part of the event-sourced spine
// described in sync.CAT.3, so the constant lives next to the writer
// types as a type-level marker. Generic sync code can reach this
// constant without depending on the path registry.
const SyncCategory = synccat.CategoryEventSourced
