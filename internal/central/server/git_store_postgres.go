package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// PostgresGitStore is the durable GitStore (sync.API.4) backed
// by the git_entities table introduced in schema step 6. It
// satisfies the GitStore interface and the optional
// GitWorkspacesLister surface the central web shell's
// workspaces-index handler queries.
//
// The store reuses the events PostgresStore's pool + transaction
// helpers so RLS scoping (app.current_org_id) and the workspace
// binding (first-push-wins) carry over from the existing tenant
// machinery — no parallel migration or pool to manage.
type PostgresGitStore struct {
	parent *PostgresStore
}

// NewPostgresGitStore returns a GitStore backed by the same
// connection pool the events store uses. The pool's migrations
// must already include step 6 (git_entities); the parent's
// migrate() runs every step on startup, so this constructor is
// safe to call right after server.New with a PostgresStore.
func NewPostgresGitStore(parent *PostgresStore) *PostgresGitStore {
	return &PostgresGitStore{parent: parent}
}

// Get returns the current revision of (workspaceID, path) or
// ErrUnknownGitEntity when no row exists for the request's org.
// Org scoping comes from the request context via the same
// withOrgScope wrapper the events Since uses.
func (s *PostgresGitStore) Get(ctx context.Context, workspaceID, path string) (proto.GitEntity, error) {
	if workspaceID == "" {
		return proto.GitEntity{}, errors.New("server: git get requires a non-empty workspace_id")
	}
	var out proto.GitEntity
	err := s.parent.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)
		return tx.QueryRow(ctx, `
			SELECT path, revision, content, signature, actor, updated_at
			FROM   git_entities
			WHERE  org_id = $1 AND workspace_id = $2 AND path = $3
		`, orgID, workspaceID, path).Scan(
			&out.Path, &out.Revision, &out.Content,
			&out.Signature, &out.Actor, &out.UpdatedAt,
		)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return proto.GitEntity{}, fmt.Errorf("%w: %q", ErrUnknownGitEntity, path)
		}
		return proto.GitEntity{}, fmt.Errorf("server: git get: %w", err)
	}
	return out, nil
}

// Put inserts or updates (workspaceID, rec.Path) against the
// supplied baseRevision. Mirror of MemoryGitStore.Put — the
// branches are:
//
//	!exists && baseRev == ""  → insert (initial creation)
//	!exists && baseRev != ""  → conflict (parent claimed; we have nothing)
//	exists  && cur == baseRev → update (or no-op if cur == new)
//	exists  && cur != baseRev → conflict (return current to client)
//
// The workspace row is ensured on first push (idempotent
// ON CONFLICT DO NOTHING) so a fresh workspace doesn't need a
// prior events push to satisfy the workspaces FK. The org
// binding ride-through matches the events flow's
// enforceWorkspaceBinding (handler-level — cross-org pushes get
// 403 upstream of this method).
func (s *PostgresGitStore) Put(ctx context.Context, workspaceID string, rec proto.GitEntity, baseRevision string) error {
	if workspaceID == "" {
		return errors.New("server: git put requires a non-empty workspace_id")
	}
	if rec.Path == "" {
		return errors.New("server: git put requires a non-empty path")
	}
	if rec.Revision == "" {
		return errors.New("server: git put requires a non-empty revision")
	}
	return s.parent.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)

		// Ensure the workspace row exists — mirrors the events
		// Append path so a brand-new workspace can land its first
		// git entity without a prior events push.
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspaces (id, org_id, first_actor)
			VALUES ($1, $2, $3)
			ON CONFLICT (id) DO NOTHING
		`, workspaceID, orgID, rec.Actor); err != nil {
			return fmt.Errorf("server: bind workspace: %w", err)
		}

		// Lock the row if it exists so the read-modify-write below
		// is consistent under concurrent pushes.
		var current proto.GitEntity
		err := tx.QueryRow(ctx, `
			SELECT path, revision, content, signature, actor, updated_at
			FROM   git_entities
			WHERE  org_id = $1 AND workspace_id = $2 AND path = $3
			FOR UPDATE
		`, orgID, workspaceID, rec.Path).Scan(
			&current.Path, &current.Revision, &current.Content,
			&current.Signature, &current.Actor, &current.UpdatedAt,
		)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("server: git put lock: %w", err)
		}

		switch {
		case !exists && baseRevision != "":
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

		now := time.Now().UTC()
		if exists {
			if _, err := tx.Exec(ctx, `
				UPDATE git_entities
				SET    revision = $4, content = $5, signature = $6, actor = $7, updated_at = $8
				WHERE  org_id = $1 AND workspace_id = $2 AND path = $3
			`, orgID, workspaceID, rec.Path,
				rec.Revision, rec.Content, rec.Signature, rec.Actor, now,
			); err != nil {
				return fmt.Errorf("server: git put update: %w", err)
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO git_entities (
				org_id, workspace_id, path, revision, content, signature, actor, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, orgID, workspaceID, rec.Path,
			rec.Revision, rec.Content, rec.Signature, rec.Actor, now,
		); err != nil {
			return fmt.Errorf("server: git put insert: %w", err)
		}
		return nil
	})
}

// List returns the paths stored for (request-org, workspaceID),
// sorted in lex order. An unknown workspaceID returns an empty
// slice + nil error to mirror MemoryGitStore.
func (s *PostgresGitStore) List(ctx context.Context, workspaceID string) ([]string, error) {
	if workspaceID == "" {
		return nil, errors.New("server: git list requires a non-empty workspace_id")
	}
	var out []string
	err := s.parent.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)
		rows, err := tx.Query(ctx, `
			SELECT path
			FROM   git_entities
			WHERE  org_id = $1 AND workspace_id = $2
			ORDER  BY path
		`, orgID, workspaceID)
		if err != nil {
			return fmt.Errorf("server: git list: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return fmt.Errorf("server: git list scan: %w", err)
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListWorkspaces returns every workspace id the request's org
// holds git content for, sorted in lex order. Satisfies the
// central web shell's GitWorkspacesLister opt-in interface so
// the /orgs/<id>/workspaces page can enumerate without a
// separate workspaces-table query.
//
// Unlike Get/Put/List this method has no ctx-scoped workspace —
// it returns the set of workspaces, which is the input the
// caller uses to subsequently scope by. The org filter still
// applies via withOrgScope.
func (s *PostgresGitStore) ListWorkspaces(ctx context.Context, orgID string) ([]string, error) {
	if orgID == "" {
		return nil, fmt.Errorf("server: ListWorkspaces requires orgID")
	}
	ctx = WithOrgID(ctx, orgID)
	var out []string
	err := s.parent.withOrgScope(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT workspace_id
			FROM   git_entities
			WHERE  org_id = $1
		`, orgID)
		if err != nil {
			return fmt.Errorf("server: git list-workspaces: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("server: git list-workspaces scan: %w", err)
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
