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
// Every operation is scoped by workspaceID — a central node holds
// content for multiple workspaces, and entities pushed for one
// workspace are invisible to reads from another (storage.WS.1's
// per-workspace `.rex/` model holds on central too).
//
// Method semantics:
//
//	Get: returns the current revision of (workspaceID, path) or
//	     ErrUnknownGitEntity.
//	Put: stores rec atomically against (workspaceID, rec.Path);
//	     baseRevision must match the current revision or returns
//	     *GitRevisionConflictError with the server-side current
//	     revision filled in. An empty baseRevision is valid only
//	     when the entity has never been pushed for this workspace
//	     (initial creation).
//	List: returns the paths the store holds for workspaceID in
//	      lex order. An unknown workspaceID returns an empty
//	      slice + nil error.
type GitStore interface {
	Get(ctx context.Context, workspaceID, path string) (proto.GitEntity, error)
	Put(ctx context.Context, workspaceID string, rec proto.GitEntity, baseRevision string) error
	List(ctx context.Context, workspaceID string) ([]string, error)
}

// MemoryGitStore is the in-memory GitStore implementation. Backed
// by a map[workspaceID]map[path]GitEntity and one RWMutex; matches
// the bring-up scale the in-process central targets. Postgres
// durability for git_entities is the follow-up under
// central-node.DB.* — see the sync.yaml task note.
type MemoryGitStore struct {
	mu        sync.RWMutex
	workspace map[string]map[string]proto.GitEntity
}

// NewMemoryGitStore returns an empty in-memory GitStore.
func NewMemoryGitStore() *MemoryGitStore {
	return &MemoryGitStore{workspace: make(map[string]map[string]proto.GitEntity)}
}

// Get returns the current revision of (workspaceID, path), or
// ErrUnknownGitEntity when the entity has never been pushed for
// the workspace.
func (s *MemoryGitStore) Get(_ context.Context, workspaceID, path string) (proto.GitEntity, error) {
	if workspaceID == "" {
		return proto.GitEntity{}, errors.New("server: git get requires a non-empty workspace_id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ws, ok := s.workspace[workspaceID]
	if !ok {
		return proto.GitEntity{}, fmt.Errorf("%w: %q", ErrUnknownGitEntity, path)
	}
	rec, ok := ws[path]
	if !ok {
		return proto.GitEntity{}, fmt.Errorf("%w: %q", ErrUnknownGitEntity, path)
	}
	return rec, nil
}

// Put stores rec under (workspaceID, rec.Path) if baseRevision
// matches the current revision. On mismatch returns
// *GitRevisionConflictError; the store is unmodified.
//
// Idempotent under retry: a Put whose baseRevision matches AND
// whose rec.Revision equals the current revision is a no-op (the
// same client retrying after a flaky network sees the same
// outcome).
func (s *MemoryGitStore) Put(_ context.Context, workspaceID string, rec proto.GitEntity, baseRevision string) error {
	if workspaceID == "" {
		return errors.New("server: git put requires a non-empty workspace_id")
	}
	if rec.Path == "" {
		return errors.New("server: git put requires a non-empty path")
	}
	if rec.Revision == "" {
		return errors.New("server: git put requires a non-empty revision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.workspace[workspaceID]
	current, exists := ws[rec.Path]
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
	if ws == nil {
		ws = make(map[string]proto.GitEntity)
		s.workspace[workspaceID] = ws
	}
	ws[rec.Path] = rec
	return nil
}

// List returns every entity path stored for workspaceID, in lex
// order. An unknown workspaceID returns an empty slice + nil error
// — the read surface treats "no entries yet" and "unknown
// workspace" identically because the eventual Postgres
// implementation cannot cheaply distinguish them.
func (s *MemoryGitStore) List(_ context.Context, workspaceID string) ([]string, error) {
	if workspaceID == "" {
		return nil, errors.New("server: git list requires a non-empty workspace_id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ws, ok := s.workspace[workspaceID]
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(ws))
	for k := range ws {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// ListWorkspaces returns every workspace id the store has at
// least one entity for, in lex order. Used by the central web
// shell's workspaces index when no Postgres workspaces table is
// available (MemoryStore dev mode). PostgresStore-backed
// deployments query the workspaces table directly.
func (s *MemoryGitStore) ListWorkspaces() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.workspace))
	for k := range s.workspace {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
