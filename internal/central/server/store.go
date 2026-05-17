package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// ErrUnknownCursor is returned by Since when the supplied cursor
// does not match any event the store has observed. The client must
// treat this as a hard divergence and resync from empty.
var ErrUnknownCursor = errors.New("server: unknown cursor")

// Store is the persistence interface the central server depends
// on for the sync surface (push + pull). v1 ships two
// implementations: the in-memory MemoryStore (zero deps, used by
// dev/test paths and the bring-up server) and PostgresStore
// (durable, the real central deployment target — see
// central-node.DB.*).
//
// Method semantics match the existing in-memory contract:
//
//	Head:        id of the latest record in insertion order, or "" empty.
//	Append:      idempotent; (added=true) on a fresh id, (added=false)
//	             on a duplicate. Used to enable sync.API.6 (push is
//	             safe to retry).
//	AppendBatch: bulk-path equivalent of Append: returns the ids of
//	             the records that landed as fresh inserts (length =
//	             accepted count; duplicates = len(recs) - len(added)).
//	             Returning IDs (not just a count) lets the handler
//	             keep per-type metrics precise even when the
//	             database call is collapsed into a single round
//	             trip. Implementations MUST be atomic — either every
//	             fresh record lands or none, so a partial-success
//	             failure mode never leaks (the per-request response
//	             only reports one accepted / duplicates pair
//	             anyway). Push hot-path and the audit appender both
//	             call through here so any multi-event operation
//	             amortises the per-record round-trip cost (Postgres
//	             impl in particular: one tx + one workspace upsert
//	             per distinct id + one multi-row INSERT instead of
//	             N×(BEGIN + INSERT + COMMIT)).
//	Since:       records strictly after the cursor in insertion order.
//	             Empty cursor = everything; unknown cursor =
//	             ErrUnknownCursor (hard divergence).
//	Len:         total record count; informational.
type Store interface {
	Head(ctx context.Context) (string, error)
	Append(ctx context.Context, rec eventlog.Record) (added bool, err error)
	AppendBatch(ctx context.Context, recs []eventlog.Record) (addedIDs []string, err error)
	Since(ctx context.Context, cursor string) ([]eventlog.Record, error)
	Len(ctx context.Context) (int, error)
}

// MemoryStore is the in-memory Store implementation. Every
// mutation is guarded by a single RWMutex; this is fine for the
// bring-up scale we target with the in-process central. The
// PostgresStore covers production-scale durability.
type MemoryStore struct {
	mu      sync.RWMutex
	records []eventlog.Record
	byID    map[string]int // record id -> index into records
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: make(map[string]int)}
}

// NewStore is the historical constructor name; kept as an alias
// so existing test code and the cmd/rex-central bring-up path
// continue to compile.
func NewStore() *MemoryStore {
	return NewMemoryStore()
}

// Head returns the id of the latest record, or "" when the
// store is empty.
func (s *MemoryStore) Head(_ context.Context) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.records) == 0 {
		return "", nil
	}
	return s.records[len(s.records)-1].ID, nil
}

// Append persists rec if its id is new. Returns added=true on a
// fresh record, added=false on a duplicate.
func (s *MemoryStore) Append(_ context.Context, rec eventlog.Record) (bool, error) {
	if rec.ID == "" {
		return false, errors.New("server: append requires a non-empty record id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[rec.ID]; dup {
		return false, nil
	}
	s.byID[rec.ID] = len(s.records)
	s.records = append(s.records, rec)
	return true, nil
}

// AppendBatch is the memory store's bulk path: a single lock
// covers the whole slice so all-or-nothing atomicity matches the
// Postgres impl's transactional guarantee. Duplicates (records
// whose id already exists) are skipped silently and excluded
// from the returned slice, so idempotent re-pushes work the
// same as the per-record path.
func (s *MemoryStore) AppendBatch(_ context.Context, recs []eventlog.Record) ([]string, error) {
	if len(recs) == 0 {
		return nil, nil
	}
	for _, rec := range recs {
		if rec.ID == "" {
			return nil, errors.New("server: append requires a non-empty record id")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	added := make([]string, 0, len(recs))
	for _, rec := range recs {
		if _, dup := s.byID[rec.ID]; dup {
			continue
		}
		s.byID[rec.ID] = len(s.records)
		s.records = append(s.records, rec)
		added = append(added, rec.ID)
	}
	return added, nil
}

// Since returns the slice of records strictly after cursor in
// insertion order. An empty cursor means "everything"; an
// unknown cursor returns ErrUnknownCursor.
func (s *MemoryStore) Since(_ context.Context, cursor string) ([]eventlog.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cursor == "" {
		out := make([]eventlog.Record, len(s.records))
		copy(out, s.records)
		return out, nil
	}
	idx, ok := s.byID[cursor]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownCursor, cursor)
	}
	tail := s.records[idx+1:]
	out := make([]eventlog.Record, len(tail))
	copy(out, tail)
	return out, nil
}

// SinceForWorkspace mirrors Since but filters to records whose
// WorkspaceID matches workspaceID. Memory implementation does the
// filter inline so call sites that prefer the workspace-scoped
// surface get the same shape from both stores.
func (s *MemoryStore) SinceForWorkspace(_ context.Context, workspaceID, cursor string) ([]eventlog.Record, error) {
	if workspaceID == "" {
		return nil, errors.New("server: SinceForWorkspace requires a non-empty workspace_id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	start := 0
	if cursor != "" {
		idx, ok := s.byID[cursor]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCursor, cursor)
		}
		start = idx + 1
	}
	tail := s.records[start:]
	out := make([]eventlog.Record, 0, len(tail))
	for _, r := range tail {
		if r.WorkspaceID == workspaceID {
			out = append(out, r)
		}
	}
	return out, nil
}

// Len returns the total number of records currently held.
func (s *MemoryStore) Len(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records), nil
}
