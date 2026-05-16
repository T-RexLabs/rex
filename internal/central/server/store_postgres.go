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

// withOrgScope opens a transaction, sets app.current_org_id
// to the value stamped on ctx, runs fn against the tx, and
// commits on success. The setting is transaction-local
// (set_config(.., true)) so it auto-clears at commit/rollback
// and never leaks to the next caller borrowing this connection
// from the pool.
//
// Used by every PostgresStore method that touches an
// RLS-protected table (events, workspaces) since schema step 4.
// fn returns an error to roll back; otherwise the tx commits.
func (s *PostgresStore) withOrgScope(ctx context.Context, fn func(pgx.Tx) error) error {
	orgID := OrgIDFromContext(ctx)
	if orgID == "" {
		return errors.New("server: missing org id on context (use WithOrgID)")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("server: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // commit overrides
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.current_org_id', $1, true)`,
		orgID,
	); err != nil {
		return fmt.Errorf("server: set org scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("server: commit tx: %w", err)
	}
	return nil
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
// for the request's org. Three layers of scoping:
//
//  1. WithOrgID requires an org id on ctx (application gate).
//  2. The WHERE org_id = $1 filter on the query.
//  3. Postgres RLS rules from schema v4 — defense in depth.
//
// All three must agree; if app code forgets either of the
// first two, RLS catches it.
func (s *PostgresStore) Head(ctx context.Context) (string, error) {
	var id string
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx) // already validated by withOrgScope
		serr := tx.QueryRow(ctx,
			`SELECT id FROM events WHERE org_id = $1 ORDER BY insertion_seq DESC LIMIT 1`,
			orgID,
		).Scan(&id)
		if serr != nil && !errors.Is(serr, pgx.ErrNoRows) {
			return fmt.Errorf("server: head: %w", serr)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Append inserts rec, idempotent on the id PK. Returns
// added=true on a fresh insert, added=false on a duplicate.
//
// Multi-tenant scoping happens in withOrgScope: the tx sets
// app.current_org_id from ctx so the RLS WITH CHECK clause
// passes on the INSERT. The application-level WHERE org_id
// filter on Head/Since/Len is now redundant given RLS, but we
// keep it — three independent layers (ctx gate, WHERE clause,
// RLS) is the structural promise of DB.2's "defense-in-depth".
func (s *PostgresStore) Append(ctx context.Context, rec eventlog.Record) (bool, error) {
	if rec.ID == "" {
		return false, errors.New("server: append requires a non-empty record id")
	}
	payload := rec.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}

	var added bool
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)

		// Workspace binding: idempotent on (id) PK. ON CONFLICT
		// DO NOTHING means a second push from a different org
		// silently keeps the original binding; the tenant
		// middleware checks the binding earlier and surfaces
		// the org mismatch with 403 before Append is reached.
		if rec.WorkspaceID != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO workspaces (id, org_id, first_actor)
				VALUES ($1, $2, $3)
				ON CONFLICT (id) DO NOTHING
			`, rec.WorkspaceID, orgID, rec.Actor); err != nil {
				return fmt.Errorf("server: bind workspace: %w", err)
			}
		}

		var id string
		serr := tx.QueryRow(ctx, `
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
		if serr != nil {
			if errors.Is(serr, pgx.ErrNoRows) {
				added = false
				return nil // duplicate id
			}
			return fmt.Errorf("server: append: %w", serr)
		}
		added = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return added, nil
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
// cursor returns every row in the org; unknown cursor (or a
// cursor pointing at another org's row) returns ErrUnknownCursor.
// All queries run inside withOrgScope so RLS provides the same
// scoping as the WHERE clause.
func (s *PostgresStore) Since(ctx context.Context, cursor string) ([]eventlog.Record, error) {
	var out []eventlog.Record
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)
		var (
			rows pgx.Rows
			qerr error
		)
		if cursor == "" {
			rows, qerr = tx.Query(ctx, `
				SELECT id, hlc_wall, hlc_logical,
				       type, version, actor, workspace_id, payload, signature
				FROM events
				WHERE org_id = $1
				ORDER BY insertion_seq
			`, orgID)
		} else {
			rows, qerr = tx.Query(ctx, `
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
		if qerr != nil {
			return fmt.Errorf("server: since: %w", qerr)
		}
		defer rows.Close()

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
				return fmt.Errorf("server: since scan: %w", err)
			}
			rec.Payload = json.RawMessage(pay)
			out = append(out, rec)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("server: since iter: %w", err)
		}

		// If a non-empty cursor returned 0 rows, distinguish
		// "cursor is the head" from "cursor was never seen in
		// this org". The existence check is org-scoped.
		if cursor != "" && len(out) == 0 {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM events WHERE id = $1 AND org_id = $2)`,
				cursor, orgID,
			).Scan(&exists); err != nil {
				return fmt.Errorf("server: since cursor-exists: %w", err)
			}
			if !exists {
				return fmt.Errorf("%w: %q", ErrUnknownCursor, cursor)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SinceForWorkspace returns events for the request's org that
// belong to workspaceID, in insertion order. The cursor semantics
// match Since: empty cursor returns the entire workspace history;
// a non-empty cursor returns events strictly after it (and the
// cursor must exist in the same org or ErrUnknownCursor fires).
//
// This is the WHERE-pushdown analog of doing Since(ctx, cursor)
// then filtering rec.WorkspaceID in Go — the web shell's
// runs/audit projections prefer this when available so multi-
// workspace orgs don't ship every workspace's events over the wire
// just to render one workspace's view.
func (s *PostgresStore) SinceForWorkspace(ctx context.Context, workspaceID, cursor string) ([]eventlog.Record, error) {
	if workspaceID == "" {
		return nil, errors.New("server: SinceForWorkspace requires a non-empty workspace_id")
	}
	var out []eventlog.Record
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)
		var (
			rows pgx.Rows
			qerr error
		)
		if cursor == "" {
			rows, qerr = tx.Query(ctx, `
				SELECT id, hlc_wall, hlc_logical,
				       type, version, actor, workspace_id, payload, signature
				FROM events
				WHERE org_id = $1 AND workspace_id = $2
				ORDER BY insertion_seq
			`, orgID, workspaceID)
		} else {
			rows, qerr = tx.Query(ctx, `
				WITH cur AS (SELECT insertion_seq FROM events WHERE id = $1 AND org_id = $2)
				SELECT id, hlc_wall, hlc_logical,
				       type, version, actor, workspace_id, payload, signature
				FROM events
				WHERE org_id = $2
				  AND workspace_id = $3
				  AND EXISTS (SELECT 1 FROM cur)
				  AND insertion_seq > (SELECT insertion_seq FROM cur)
				ORDER BY insertion_seq
			`, cursor, orgID, workspaceID)
		}
		if qerr != nil {
			return fmt.Errorf("server: since-for-workspace: %w", qerr)
		}
		defer rows.Close()
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
				return fmt.Errorf("server: since-for-workspace scan: %w", err)
			}
			rec.Payload = json.RawMessage(pay)
			out = append(out, rec)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("server: since-for-workspace iter: %w", err)
		}
		if cursor != "" && len(out) == 0 {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM events WHERE id = $1 AND org_id = $2)`,
				cursor, orgID,
			).Scan(&exists); err != nil {
				return fmt.Errorf("server: since-for-workspace cursor-exists: %w", err)
			}
			if !exists {
				return fmt.Errorf("%w: %q", ErrUnknownCursor, cursor)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Len returns the row count for the request's org. An unscoped
// ctx errors rather than counting all rows — same defense-in-
// depth shape as Append/Since/Head.
func (s *PostgresStore) Len(ctx context.Context) (int, error) {
	var n int
	err := s.withOrgScope(ctx, func(tx pgx.Tx) error {
		orgID := OrgIDFromContext(ctx)
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM events WHERE org_id = $1`,
			orgID,
		).Scan(&n)
	})
	if err != nil {
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

	// 4: Row Level Security policies on multi-tenant tables
	//    (DB.2 "enforced by row-level security policies as a
	//    defense-in-depth", task tenant-rls).
	//
	// Policy shape: a row passes when its org_id (as text)
	// equals app.current_org_id (set per-transaction by the
	// withOrgScope helper). current_setting(name, true)
	// returns NULL when unset; NULL = anything yields NULL →
	// false → zero rows pass. Forgetting to scope means seeing
	// nothing rather than seeing everything.
	//
	// ENABLE without FORCE: the rex app is the table owner via
	// the migration, so it bypasses RLS by default. The
	// application middleware (tenant-routing) does primary
	// scoping via WHERE org_id = $1 + the workspace binding
	// check; RLS exists for two reasons:
	//
	//   1. Defense in depth for non-owner Postgres clients —
	//      a future read-only audit role or a misconfigured
	//      direct psql session sees only rows for the org it
	//      explicitly scopes to.
	//   2. Schema-level documentation — the policy makes the
	//      tenancy model visible from \d events, surfacing
	//      org_id as the partitioning column for any future
	//      reader.
	//
	// FORCE was considered but it conflates "no binding" with
	// "binding belongs to another org" for the middleware's
	// cross-org workspace check. Without that distinction the
	// middleware can't enforce ORG.6-note's first-push-wins
	// rule. The trade is: weaker RLS for stronger application-
	// layer correctness. A future PR adding BYPASSRLS-aware
	// roles can flip FORCE on without touching the middleware.
	`
		ALTER TABLE events     ENABLE ROW LEVEL SECURITY;
		ALTER TABLE workspaces ENABLE ROW LEVEL SECURITY;

		CREATE POLICY events_org_isolation ON events
			USING      (org_id::text = current_setting('app.current_org_id', true))
			WITH CHECK (org_id::text = current_setting('app.current_org_id', true));

		CREATE POLICY workspaces_org_isolation ON workspaces
			USING      (org_id::text = current_setting('app.current_org_id', true))
			WITH CHECK (org_id::text = current_setting('app.current_org_id', true));
	`,

	// 5: admin bootstrap (central-node.BOOT.1, BOOT.2).
	//
	// admin_bootstrap holds the one-time claim token that the
	// first user redeems to become the founder admin of the
	// default org. Exactly one row is seeded on the first
	// migration where no admin exists; subsequent startups
	// re-read the existing row rather than minting a new one
	// (the token survives restarts so an operator who lost the
	// log can still grab it).
	//
	// The seed uses gen_random_uuid()::text as the token —
	// 36 chars of hex with hyphens, plenty of entropy for the
	// "redeem once" flow. Production deployments that want
	// tighter security can rotate via a separate ops command;
	// not in scope for v1.
	`
		CREATE TABLE IF NOT EXISTS admin_bootstrap (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			token       TEXT NOT NULL UNIQUE,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			redeemed_at TIMESTAMPTZ,
			redeemed_by TEXT
		);

		-- Seed exactly one token row when no admin exists in
		-- any org. The check is on org_memberships.role to be
		-- robust against future "founder" or org-owner roles —
		-- whatever role an admin holds, this looks for it.
		INSERT INTO admin_bootstrap (token)
		SELECT gen_random_uuid()::text
		WHERE NOT EXISTS (
			SELECT 1 FROM org_memberships WHERE role = 'admin'
		)
		AND NOT EXISTS (SELECT 1 FROM admin_bootstrap);
	`,
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
