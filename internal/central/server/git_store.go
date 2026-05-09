package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// ErrUnknownGitEntity is returned by GitStore.Get when the entity
// has never been pushed. Surfaces as a 404 on GET /sync/git/<path>.
var ErrUnknownGitEntity = errors.New("server: unknown git entity")

// GitRevisionConflictError is returned by GitStore.Put when the
// supplied baseRevision does not match the current revision for
// the entity. Surfaces as a 409 on POST /sync/git.
//
// The ServerCurrent field carries the in-store revision so the
// handler can return it to the client without a second round
// trip to the store.
type GitRevisionConflictError struct {
	Path          string
	ServerCurrent proto.GitEntity
}

// Error reports the conflict in the form the central log lines emit.
func (e *GitRevisionConflictError) Error() string {
	return fmt.Sprintf("server: git revision conflict for %q (server has %q)",
		e.Path, e.ServerCurrent.Revision)
}

// GitStore is the persistence interface for git-merged content
// (sync.CAT.2 / sync.API.4). The events Store and the GitStore are
// independent surfaces — the central node serves /sync/events from
// the former and /sync/git from this one.
//
// Method semantics:
//
//	Get: returns the current revision of path or ErrUnknownGitEntity.
//	Put: stores rec atomically; baseRevision must match the current
//	     revision of rec.Path or returns *GitRevisionConflictError with
//	     the server-side current revision filled in. An empty
//	     baseRevision is valid only when the entity has never been
//	     pushed (initial creation).
//	List: returns all entity paths the store currently holds, in lex
//	      order. Used by the bring-up status surface and tests.
type GitStore interface {
	Get(ctx context.Context, path string) (proto.GitEntity, error)
	Put(ctx context.Context, rec proto.GitEntity, baseRevision string) error
	List(ctx context.Context) ([]string, error)
}

// MemoryGitStore is the in-memory GitStore implementation. Backed by a
// map[path]GitEntity and one RWMutex; matches the bring-up scale the
// in-process central targets. Postgres durability for git_entities is
// the follow-up under central-node.DB.* — see the sync.yaml task note.
type MemoryGitStore struct {
	mu      sync.RWMutex
	entries map[string]proto.GitEntity
}

// NewMemoryGitStore returns an empty in-memory GitStore.
func NewMemoryGitStore() *MemoryGitStore {
	return &MemoryGitStore{entries: make(map[string]proto.GitEntity)}
}

// Get returns the current revision of path or ErrUnknownGitEntity.
func (s *MemoryGitStore) Get(_ context.Context, path string) (proto.GitEntity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.entries[path]
	if !ok {
		return proto.GitEntity{}, fmt.Errorf("%w: %q", ErrUnknownGitEntity, path)
	}
	return rec, nil
}

// Put stores rec if baseRevision matches the current revision for
// rec.Path. On mismatch returns *GitRevisionConflictError; the store
// is unmodified.
//
// Idempotent under retry: a Put whose baseRevision matches AND whose
// rec.Revision equals the current revision is a no-op (the same client
// retrying after a flaky network sees the same outcome).
func (s *MemoryGitStore) Put(_ context.Context, rec proto.GitEntity, baseRevision string) error {
	if rec.Path == "" {
		return errors.New("server: git put requires a non-empty path")
	}
	if rec.Revision == "" {
		return errors.New("server: git put requires a non-empty revision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.entries[rec.Path]
	switch {
	case !exists && baseRevision != "":
		// Client thinks there's a parent revision but we have
		// nothing — surface as a conflict so the client resyncs.
		return &GitRevisionConflictError{
			Path:          rec.Path,
			ServerCurrent: proto.GitEntity{Path: rec.Path},
		}
	case exists && current.Revision != baseRevision:
		return &GitRevisionConflictError{
			Path:          rec.Path,
			ServerCurrent: current,
		}
	}
	s.entries[rec.Path] = rec
	return nil
}

// List returns every entity path in the store in lex order.
func (s *MemoryGitStore) List(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.entries))
	for k := range s.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}
