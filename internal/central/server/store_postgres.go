package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// PostgresStore is the durable Store backed by a Postgres
// instance. Every record lands in the events table with a
// monotonic insertion_seq column that preserves the order in
// which the central node observed each record — that order is
// what Since() ranges over, and it does not depend on the HLC
// clock fields (which are useful for cross-node ordering but
// not for "what came after id X on this server").
//
// One row per record. Idempotent append uses INSERT ... ON
// CONFLICT (id) DO NOTHING + RETURNING id; an empty result
// means a duplicate. Cursor lookups join on insertion_seq via
// a subquery so the cursor's id can resolve in one round trip.
//
// v1 is single-tenant: there is no org_id on rows. Multi-tenant
// scoping (central-node.TENANT.*) lands in a separate task
// adding the column + RLS policies.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects to dsn (any libpq-style DSN —
// "postgres://user:pass@host:port/db" or KV "host=... user=...")
// and runs the schema migrator before returning. ctx bounds
// both the connect and the migration. The pool is the caller's
// to close via Close().
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("server: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("server: connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("server: ping postgres: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("server: migrate postgres: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Ping verifies the connection pool can still reach the
// database. Used by /ready to back the HEALTH.1 readiness probe;
// returns nil when the database answers and an error otherwise.
func (s *PostgresStore) Ping(ctx context.Context) error {
	if s.pool == nil {
		return fmt.Errorf("server: postgres store is closed")
	}
	return s.pool.Ping(ctx)
}

// Close releases the underlying connection pool. Safe to call
// once on shutdown; subsequent calls are no-ops.
func (s *PostgresStore) Close() {
	if s.pool != nil {
		s.pool.Close()
		s.pool = nil
	}
}

// Head returns the id of the row with the largest insertion_seq
// for the request's org (read from ctx via OrgIDFromContext), or
// empty when the org has no rows. Multi-tenant scoping: the
// query filters by org_id so cross-org peeking is impossible at
// the application layer; tenant-rls will add Postgres-level RLS
// as defense-in-depth.
func (s *PostgresStore) Head(ctx context.Context) (string, error) {
	orgID := OrgIDFromContext(ctx)
	if orgID == "" {
		return "", errors.New("server: head requires an org id on context (use WithOrgID)")
	}
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM events WHERE org_id = $1 ORDER BY insertion_seq DESC LIMIT 1`,
		orgID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("server: head: %w", err)
	}
	return id, nil
}

// Append inserts rec, idempotent on the id PK. Returns
// added=true on a fresh insert, added=false on a duplicate.
//
// Multi-tenant scoping: the request's org id must be stamped on
// ctx via WithOrgID — Append refuses an unscoped context so the
// middleware can never fail closed silently. The append runs
// inside a transaction:
//
//   1. Workspace binding (ORG.6-note "first-push-wins"): if the
//      record carries a workspace_id, INSERT the binding row
//      ON CONFLICT DO NOTHING. Subsequent pushes for the same
//      workspace_id from a member of a different org will hit
//      the workspace's stored org_id mismatch check (added with
//      tenant-routing middleware).
//   2. Insert the event row with org_id set from the context.
func (s *PostgresStore) Append(ctx context.Context, rec eventlog.Record) (bool, error) {
	if rec.ID == "" {
		return false, errors.New("server: append requires a non-empty record id")
	}
	orgID := OrgIDFromContext(ctx)
	if orgID == "" {
		return false, errors.New("server: append requires an org id on context (use WithOrgID)")
	}
	payload := rec.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("server: begin tx for append: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // commit overrides

	// Workspace binding: idempotent on (id) PK. ON CONFLICT
	// DO NOTHING means a second push from a different org
	// silently keeps the original binding; the tenant-routing
	// middleware checks the binding earlier and surfaces the
	// org mismatch with 403 before Append is reached.
	if rec.WorkspaceID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspaces (id, org_id, first_actor)
			VALUES ($1, $2, $3)
			ON CONFLICT (id) DO NOTHING
		`, rec.WorkspaceID, orgID, rec.Actor); err != nil {
			return false, fmt.Errorf("server: bind workspace: %w", err)
		}
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO events (
			id, hlc_wall, hlc_logical,
			type, version, actor, workspace_id, payload, signature, org_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO NOTHING
		RETURNING id
	`,
		rec.ID,
		rec.Timestamp.Wall,
		rec.Timestamp.Logical,
		rec.Type,
		rec.Version,
		rec.Actor,
		rec.WorkspaceID,
		[]byte(payload),
		rec.Signature,
		orgID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return false, fmt.Errorf("server: commit dup append: %w", commitErr)
			}
			return false, nil // duplicate id
		}
		return false, fmt.Errorf("server: append: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("server: commit append: %w", err)
	}
	return true, nil
}

// WorkspaceOrg returns the org id bound to a workspace_id, or
// empty + false when no binding exists. Used by the
// tenant-routing middleware to enforce ORG.6-note
// "first-push-wins" — if a binding already exists, subsequent
// pushes must come from a member of that org.
func (s *PostgresStore) WorkspaceOrg(ctx context.Context, workspaceID string) (string, bool, error) {
	var orgID string
	err := s.pool.QueryRow(ctx,
		`SELECT org_id::text FROM workspaces WHERE id = $1`,
		workspaceID,
	).Scan(&orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("server: workspace org: %w", err)
	}
	return orgID, true, nil
}

// Since returns the records strictly after cursor in
// insertion_seq order, scoped to the request's org. Empty
// cursor returns every row in the org; unknown cursor returns
// ErrUnknownCursor. The cursor's existence check is also
// org-scoped — a cursor pointing at another org's row reads as
// unknown, not as a head-mid-other-org peek.
func (s *PostgresStore) Since(ctx context.Context, cursor string) ([]eventlog.Record, error) {
	orgID := OrgIDFromContext(ctx)
	if orgID == "" {
		return nil, errors.New("server: since requires an org id on context (use WithOrgID)")
	}
	var (
		rows pgx.Rows
		err  error
	)
	if cursor == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT id, hlc_wall, hlc_logical,
			       type, version, actor, workspace_id, payload, signature
			FROM events
			WHERE org_id = $1
			ORDER BY insertion_seq
		`, orgID)
	} else {
		// CTE resolves the cursor's insertion_seq in the same
		// org; if the cursor doesn't belong to this org the CTE
		// is empty and EXISTS short-circuits to 0 rows, then the
		// trailing existence check upgrades empty to
		// ErrUnknownCursor.
		rows, err = s.pool.Query(ctx, `
			WITH cur AS (SELECT insertion_seq FROM events WHERE id = $1 AND org_id = $2)
			SELECT id, hlc_wall, hlc_logical,
			       type, version, actor, workspace_id, payload, signature
			FROM events
			WHERE org_id = $2
			  AND EXISTS (SELECT 1 FROM cur)
			  AND insertion_seq > (SELECT insertion_seq FROM cur)
			ORDER BY insertion_seq
		`, cursor, orgID)
	}
	if err != nil {
		return nil, fmt.Errorf("server: since: %w", err)
	}
	defer rows.Close()

	var out []eventlog.Record
	for rows.Next() {
		var (
			rec eventlog.Record
			pay []byte
		)
		if err := rows.Scan(
			&rec.ID,
			&rec.Timestamp.Wall,
			&rec.Timestamp.Logical,
			&rec.Type,
			&rec.Version,
			&rec.Actor,
			&rec.WorkspaceID,
			&pay,
			&rec.Signature,
		); err != nil {
			return nil, fmt.Errorf("server: since scan: %w", err)
		}
		rec.Payload = json.RawMessage(pay)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("server: since iter: %w", err)
	}

	// If a non-empty cursor returned 0 rows, distinguish "cursor
	// is the head" from "cursor was never seen in this org". The
	// existence check is org-scoped — a cursor that exists in a
	// different org reads as unknown, not as a head-mid-other-
	// org peek.
	if cursor != "" && len(out) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM events WHERE id = $1 AND org_id = $2)`,
			cursor, orgID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("server: since cursor-exists: %w", err)
		}
		if !exists {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCursor, cursor)
		}
	}
	return out, nil
}

// Len returns the row count for the request's org. An unscoped
// ctx errors rather than counting all rows — same defense-in-
// depth shape as Append/Since/Head.
func (s *PostgresStore) Len(ctx context.Context) (int, error) {
	orgID := OrgIDFromContext(ctx)
	if orgID == "" {
		return 0, errors.New("server: len requires an org id on context (use WithOrgID)")
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE org_id = $1`,
		orgID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("server: len: %w", err)
	}
	return n, nil
}

// schemaSteps holds every migration the PostgresStore knows
// about. Steps are applied in slice order; the migrator records
// the highest applied index in the rex_schema_version table so
// re-runs are no-ops.
//
// New steps APPEND only — never edit a shipped step in place,
// and never reorder. That's the same overview.SYS.4 additivity
// rule the spec format follows; adding a column is a new step,
// removing one is a different new step.
var schemaSteps = []string{
	// 1: bring-up — single events table mirroring eventlog.Record.
	// HLC has Wall + Logical fields only (eventlog.HLC); no
	// per-node tiebreaker exists in the local format. The central
	// preserves observation order via insertion_seq IDENTITY.
	`
		CREATE TABLE IF NOT EXISTS events (
			id            TEXT PRIMARY KEY,
			hlc_wall      BIGINT NOT NULL,
			hlc_logical   BIGINT NOT NULL,
			type          TEXT NOT NULL,
			version       INTEGER NOT NULL,
			actor         TEXT NOT NULL DEFAULT '',
			workspace_id  TEXT NOT NULL DEFAULT '',
			payload       JSONB NOT NULL,
			signature     TEXT NOT NULL DEFAULT '',
			insertion_seq BIGINT GENERATED BY DEFAULT AS IDENTITY UNIQUE
		);
		CREATE INDEX IF NOT EXISTS events_workspace_id_idx ON events(workspace_id);
		CREATE INDEX IF NOT EXISTS events_type_idx          ON events(type);
		CREATE INDEX IF NOT EXISTS events_insertion_seq_idx ON events(insertion_seq);
	`,

	// 2: orgs + memberships + invites
	//    (identity-and-trust.ORG.*, central-node.TENANT.4-note).
	//
	// orgs: the tenancy boundary (ORG.2). idp_config + scim_config
	// are nullable jsonb so IDP-CENTRAL bridging can land later
	// without a schema bump (overview.SYS.4 additivity).
	//
	// org_memberships: which fingerprint belongs to which org
	// with which role. Default role is "member"; the
	// identity-and-trust.RBAC engine refines this when it ships.
	//
	// org_invites: the redeem-with-public-key invite flow named
	// in ORG.5 + BOOT.3.
	//
	// gen_random_uuid() is core in Postgres 13+; rex-central
	// targets 17 (post the alpine bump). No pgcrypto extension
	// needed.
	//
	// Default org seed: a single 'default' org auto-joined by
	// every authenticated identity until BOOT.* ships real org
	// creation (TENANT.4-note). The seed is idempotent — a
	// rerun-safe INSERT WHERE NOT EXISTS so the migration stays
	// re-entrant.
	`
		CREATE TABLE IF NOT EXISTS orgs (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name          TEXT NOT NULL UNIQUE,
			display_name  TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			idp_config    JSONB,
			scim_config   JSONB
		);

		CREATE TABLE IF NOT EXISTS org_memberships (
			org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			fingerprint  TEXT NOT NULL,
			role         TEXT NOT NULL DEFAULT 'member',
			joined_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (org_id, fingerprint)
		);
		CREATE INDEX IF NOT EXISTS org_memberships_fingerprint_idx
			ON org_memberships(fingerprint);

		CREATE TABLE IF NOT EXISTS org_invites (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			invited_by   TEXT NOT NULL,
			token        TEXT NOT NULL UNIQUE,
			role         TEXT NOT NULL DEFAULT 'member',
			expires_at   TIMESTAMPTZ NOT NULL,
			redeemed_at  TIMESTAMPTZ,
			redeemed_by  TEXT
		);
		CREATE INDEX IF NOT EXISTS org_invites_pending_idx
			ON org_invites(token) WHERE redeemed_at IS NULL;

		INSERT INTO orgs (name, display_name)
			SELECT 'default', 'Default organization'
			WHERE NOT EXISTS (SELECT 1 FROM orgs WHERE name = 'default');
	`,

	// 3: workspaces table + org_id column on events
	//    (central-node.TENANT.1, identity-and-trust.ORG.6,
	//    DB.2's "every row in every multi-tenant table carries
	//    an org_id column").
	//
	// workspaces is the binding between workspace_id strings
	// (free-form text on the event payload) and the org that
	// owns them. ORG.6-note: first-push-wins — the row is
	// created on first observation; subsequent pushes for the
	// same workspace_id must come from members of the same org.
	//
	// Backfill: existing single-tenant deployments bound to
	// schema v1 have events.workspace_id values with no
	// matching workspaces row. The migration walks events,
	// creates a workspaces row per distinct id (bound to
	// 'default' org), then backfills events.org_id from
	// workspaces.org_id, then makes events.org_id NOT NULL.
	`
		CREATE TABLE IF NOT EXISTS workspaces (
			id           TEXT PRIMARY KEY,
			org_id       UUID NOT NULL REFERENCES orgs(id),
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			first_actor  TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS workspaces_org_id_idx ON workspaces(org_id);

		INSERT INTO workspaces (id, org_id)
			SELECT DISTINCT e.workspace_id, o.id
			FROM   events e
			CROSS JOIN orgs o
			WHERE  e.workspace_id <> ''
			AND    o.name = 'default'
		ON CONFLICT (id) DO NOTHING;

		ALTER TABLE events ADD COLUMN IF NOT EXISTS org_id UUID;

		UPDATE events e SET org_id = w.org_id
		FROM   workspaces w
		WHERE  e.workspace_id = w.id
		AND    e.org_id IS NULL;

		UPDATE events SET org_id = (SELECT id FROM orgs WHERE name = 'default')
		WHERE  org_id IS NULL;

		ALTER TABLE events ALTER COLUMN org_id SET NOT NULL;
		ALTER TABLE events ADD CONSTRAINT events_org_id_fkey
			FOREIGN KEY (org_id) REFERENCES orgs(id);
		CREATE INDEX IF NOT EXISTS events_org_id_idx ON events(org_id);
	`,

	// NOTE: schema v4 (Row Level Security) is deferred to a
	// dedicated tenant-rls task. RLS is defense-in-depth on top
	// of the application-level middleware that ships in v3 +
	// the workspaces binding; shipping it here would force a
	// broader Store-method refactor (SET LOCAL on every query
	// inside a transaction) that doesn't fit the
	// tenant-routing scope. See specs/_proposed/_accepted/
	// central-node-amendment-2026-05-04-tenant-rls-split.yaml
	// for the rationale.
}

// DefaultOrgName is the seeded org's name. Used by
// EnsureDefaultMembership and tests; constants live near the
// migration that creates the row.
const DefaultOrgName = "default"

// migrate runs every schemaStep whose 1-based index is greater
// than the value in rex_schema_version. Idempotent: a freshly
// migrated database re-runs as a no-op. Each step runs inside a
// transaction so a partial application doesn't advance the
// version counter.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS rex_schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create rex_schema_version: %w", err)
	}
	var current int
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM rex_schema_version`,
	).Scan(&current); err != nil {
		return fmt.Errorf("read rex_schema_version: %w", err)
	}
	for i, sql := range schemaSteps {
		v := i + 1 // 1-based to match the convention readers expect.
		if v <= current {
			continue
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration v%d: %w", v, err)
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration v%d: %w", v, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO rex_schema_version (version) VALUES ($1)`, v,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration v%d: %w", v, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration v%d: %w", v, err)
		}
	}
	return nil
}
