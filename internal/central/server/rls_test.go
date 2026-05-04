package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// TestRLSPoliciesAreEnabled checks that the schema migration
// installed RLS on events + workspaces. Without this, a
// regression that drops the policies would silently weaken
// defense-in-depth without any test failing.
func TestRLSPoliciesAreEnabled(t *testing.T) {
	t.Parallel()
	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	for _, table := range []string{"events", "workspaces"} {
		var enabled bool
		if err := s.pool.QueryRow(ctx,
			`SELECT relrowsecurity FROM pg_class WHERE relname = $1`,
			table,
		).Scan(&enabled); err != nil {
			t.Fatalf("check rls on %s: %v", table, err)
		}
		if !enabled {
			t.Errorf("RLS is not enabled on %s", table)
		}
	}

	// Policies present on each table.
	for _, table := range []string{"events", "workspaces"} {
		var n int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_policies WHERE tablename = $1`,
			table,
		).Scan(&n); err != nil {
			t.Fatalf("check policies on %s: %v", table, err)
		}
		if n < 1 {
			t.Errorf("no RLS policies on %s", table)
		}
	}
}

// TestRLSFiresForNonOwnerRole drives the actual RLS gate by
// connecting as a non-owner Postgres role and confirming it
// can't see events from another org without setting
// app.current_org_id. This is the defense-in-depth promise of
// DB.2 — even if app code forgets the WHERE clause, RLS
// catches the miss for clients that aren't the table owner.
//
// We create a bare role (not the owner), grant it minimal
// privileges, and verify that:
//   - reads with no app.current_org_id return zero rows;
//   - reads with app.current_org_id = orgA return only orgA's
//     events.
func TestRLSFiresForNonOwnerRole(t *testing.T) {
	t.Parallel()
	s, scopedDSN := freshPostgresStore(t)
	ctx := context.Background()

	// Seed two orgs with one event each.
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO orgs (name, display_name) VALUES ('alpha', 'Alpha'), ('beta', 'Beta')`,
	); err != nil {
		t.Fatalf("seed orgs: %v", err)
	}
	alpha, _ := s.LookupOrg(ctx, "alpha")
	beta, _ := s.LookupOrg(ctx, "beta")

	for _, ev := range []struct {
		id, orgID string
	}{
		{"e-alpha", alpha.ID},
		{"e-beta", beta.ID},
	} {
		appendCtx := WithOrgID(ctx, ev.orgID)
		if _, err := s.Append(appendCtx, eventlog.Record{
			ID: ev.id, Type: "test.event", Version: 1,
			Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws-" + ev.id,
			Payload: []byte(`{}`),
		}); err != nil {
			t.Fatalf("Append %s: %v", ev.id, err)
		}
	}

	// Create a non-owner role and grant SELECT on events.
	// The role is unique per test run via the schema name to
	// avoid races across parallel tests.
	roleName := "rls_reader_" + schemaSafeName(t)
	if len(roleName) > 60 {
		roleName = roleName[:60]
	}
	for _, sql := range []string{
		`DROP ROLE IF EXISTS "` + roleName + `"`,
		`CREATE ROLE "` + roleName + `" LOGIN PASSWORD 'rls_reader'`,
		`GRANT USAGE ON SCHEMA "` + schemaForTest(t) + `" TO "` + roleName + `"`,
		`GRANT SELECT ON events, workspaces, orgs TO "` + roleName + `"`,
	} {
		if _, err := s.pool.Exec(ctx, sql); err != nil {
			t.Fatalf("setup role %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		// Reverse the GRANTs first so DROP ROLE doesn't fail
		// on dependent privileges.
		for _, sql := range []string{
			`REVOKE SELECT ON events, workspaces, orgs FROM "` + roleName + `"`,
			`REVOKE USAGE ON SCHEMA "` + schemaForTest(t) + `" FROM "` + roleName + `"`,
			`DROP ROLE IF EXISTS "` + roleName + `"`,
		} {
			_, _ = s.pool.Exec(context.Background(), sql)
		}
	})

	// Connect as the non-owner role.
	readerDSN := strings.Replace(scopedDSN, "postgres:dev", roleName+":rls_reader", 1)
	readerDSN = strings.Replace(readerDSN, "postgres://postgres:dev", "postgres://"+roleName+":rls_reader", 1)
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	readerPool, err := pgxpool.New(rctx, readerDSN)
	if err != nil {
		t.Fatalf("non-owner connect: %v", err)
	}
	defer readerPool.Close()

	mustCount := func(label string, f func(pgx.Tx) error) int {
		t.Helper()
		var n int
		err := pgx.BeginFunc(ctx, readerPool, func(tx pgx.Tx) error {
			if err := f(tx); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return n
	}

	// No app.current_org_id set → RLS USING evaluates to NULL
	// → 0 rows visible.
	if got := mustCount("unset", func(tx pgx.Tx) error { return nil }); got != 0 {
		t.Errorf("unset scope: got %d rows, want 0 (RLS should hide everything)", got)
	}

	// Set to alpha → 1 row visible.
	if got := mustCount("alpha", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, alpha.ID)
		return err
	}); got != 1 {
		t.Errorf("alpha scope: got %d rows, want 1", got)
	}

	// Set to beta → 1 row visible (the other one).
	if got := mustCount("beta", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, beta.ID)
		return err
	}); got != 1 {
		t.Errorf("beta scope: got %d rows, want 1", got)
	}
}

func schemaForTest(t *testing.T) string {
	t.Helper()
	return schemaSafeName(t)
}
