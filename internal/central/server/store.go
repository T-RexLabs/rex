package server

import (
	"errors"
	"fmt"
	"sync"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// ErrUnknownCursor is returned by Since when the supplied cursor
// does not match any event the store has observed. The client must
// treat this as a hard divergence and resync from empty.
var ErrUnknownCursor = errors.New("server: unknown cursor")

// Store is the in-memory event store. Every mutation is guarded by a
// single RWMutex; this is fine for the bring-up scale we target with
// the in-process central. Postgres-backed storage lands later behind
// the same conceptual surface (Head, Append, Since).
type Store struct {
	mu      sync.RWMutex
	records []eventlog.Record
	byID    map[string]int // record id -> index into records
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{byID: make(map[string]int)}
}

// Head returns the id of the latest record, or proto.HeadEmpty (the
// empty string) when the store is empty.
func (s *Store) Head() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.records) == 0 {
		return ""
	}
	return s.records[len(s.records)-1].ID
}

// Append persists rec if its id is new; returns added=true on a
// fresh record, added=false on a duplicate. The duplicate path is
// the structural enabler of sync.API.6 (idempotent push).
func (s *Store) Append(rec eventlog.Record) (added bool, err error) {
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

// Since returns the slice of records strictly after cursor in
// insertion order. An empty cursor means "everything"; an unknown
// cursor returns ErrUnknownCursor.
func (s *Store) Since(cursor string) ([]eventlog.Record, error) {
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

// Len returns the total number of records currently held.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}
