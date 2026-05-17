package server

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// pgDSN returns the Postgres DSN from REX_PG_TEST_DSN. Tests
// that need a real Postgres call this and t.Skip when unset so
// `go test ./...` works on a developer's laptop without Docker
// running. CI sets the env via the workflow's services: block.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("REX_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("REX_PG_TEST_DSN unset; skipping Postgres-backed test")
	}
	return dsn
}

// schemaSafeName turns a test name into a valid Postgres
// identifier (lowercase + underscores). Postgres identifiers
// max 63 chars; t.Name() can be longer for table-driven tests
// but our tests stay well under.
func schemaSafeName(t *testing.T) string {
	t.Helper()
	out := make([]byte, 0, len(t.Name())+8)
	for i := 0; i < len(t.Name()); i++ {
		c := t.Name()[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32) // tolower
		default:
			out = append(out, '_')
		}
	}
	return "rextest_" + string(out)
}

// freshPostgresStore returns a PostgresStore that operates
// inside its own per-test Postgres schema. The schema is
// dropped on test cleanup; running tests in parallel is safe
// because each one carries its own table namespace.
//
// Tests that need the default org id (every PostgresStore.Append
// since tenant-routing landed) call defaultOrgCtx(t, store) to
// build a request-shaped context. Same call site convenience as
// WithOrgID(ctx, ...) but resolves the default org once.
//
// Returns the scoped DSN so tests that re-open a connection
// (e.g. proving persistence across pool lifetimes) can reach
// the same schema.
func freshPostgresStore(t *testing.T) (*PostgresStore, string) {
	t.Helper()
	dsn := pgDSN(t)
	schema := schemaSafeName(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Setup: drop the schema if it exists, then recreate. Use a
	// dedicated short-lived pool so it doesn't clash with the
	// test pool's connections.
	cleanupPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for setup: %v", err)
	}
	for _, sql := range []string{
		`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`,
		`CREATE SCHEMA "` + schema + `"`,
	} {
		if _, err := cleanupPool.Exec(ctx, sql); err != nil {
			cleanupPool.Close()
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	cleanupPool.Close()

	var scopedDSN string
	switch {
	case dsn != "" && dsn[len(dsn)-1] == '?':
		scopedDSN = dsn + "search_path=" + schema
	case !contains(dsn, "?"):
		scopedDSN = dsn + "?search_path=" + schema
	default:
		scopedDSN = dsn + "&search_path=" + schema
	}

	store, err := NewPostgresStore(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		// Drop the schema after the test so a long-lived test
		// database doesn't accumulate per-test schemas across
		// runs. Best-effort: a failing drop is logged but
		// doesn't fail the test.
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		dropPool, err := pgxpool.New(dropCtx, dsn)
		if err != nil {
			t.Logf("post-test connect: %v", err)
			return
		}
		defer dropPool.Close()
		if _, err := dropPool.Exec(dropCtx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Logf("post-test drop schema %s: %v", schema, err)
		}
	})
	return store, scopedDSN
}

// contains is a stdlib-strings-free shim so this file's
// imports stay compact.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// defaultOrgCtx returns a context stamped with the default org's
// id — the shape PostgresStore.Append expects. Used by every
// PostgresStore test that exercises Append/Since/Head/Len. The
// default org row is seeded by schema step 2; LookupOrg is the
// canonical accessor.
func defaultOrgCtx(t *testing.T, store *PostgresStore) context.Context {
	t.Helper()
	org, err := store.LookupOrg(context.Background(), DefaultOrgName)
	if err != nil {
		t.Fatalf("LookupOrg(default): %v", err)
	}
	return WithOrgID(context.Background(), org.ID)
}

func TestPostgresStoreEmptyHead(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != "" {
		t.Fatalf("empty head: got %q", head)
	}
	n, err := s.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 0 {
		t.Fatalf("len: got %d", n)
	}
}

func TestPostgresStoreAppendIsIdempotent(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	r := mkRec("r1")
	r.Payload = []byte(`{"k":"v"}`)
	r.Timestamp = eventlog.HLC{Wall: 1700000000, Logical: 7}

	added, err := s.Append(ctx, r)
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if !added {
		t.Fatal("first append should add")
	}

	added2, err := s.Append(ctx, r)
	if err != nil {
		t.Fatalf("dup Append: %v", err)
	}
	if added2 {
		t.Fatal("duplicate append should not add")
	}

	n, err := s.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 1 {
		t.Fatalf("len after dup: got %d want 1", n)
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != "r1" {
		t.Fatalf("head: got %q", head)
	}
}

func TestPostgresStoreAppendRejectsEmptyID(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	if _, err := s.Append(context.Background(), eventlog.Record{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestPostgresStoreSinceEmptyCursorReturnsAllInOrder(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.Append(ctx, mkRec(id)); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	got, err := s.Since(ctx, "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].ID != want {
			t.Fatalf("at %d: got %q want %q", i, got[i].ID, want)
		}
	}
}

func TestPostgresStoreSinceCursorReturnsTail(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	for _, id := range []string{"a", "b", "c", "d"} {
		_, _ = s.Append(ctx, mkRec(id))
	}
	got, err := s.Since(ctx, "b")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2", len(got))
	}
	if got[0].ID != "c" || got[1].ID != "d" {
		t.Fatalf("ids: %+v", got)
	}
}

func TestPostgresStoreSinceUnknownCursorErrors(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	if _, err := s.Append(ctx, mkRec("a")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_, err := s.Since(ctx, "ghost")
	if !errors.Is(err, ErrUnknownCursor) {
		t.Fatalf("got %v want ErrUnknownCursor", err)
	}
}

func TestPostgresStoreSinceLatestReturnsEmpty(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	for _, id := range []string{"a", "b"} {
		_, _ = s.Append(ctx, mkRec(id))
	}
	got, err := s.Since(ctx, "b")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty tail, got %d records", len(got))
	}
}

func TestPostgresStorePreservesPayloadAndHLC(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	r := eventlog.Record{
		ID:          "r-payload",
		Type:        "test.payload",
		Version:     2,
		Actor:       "l-aaaaaaaaaaaaaaaa",
		WorkspaceID: "ws-roundtrip",
		Payload:     []byte(`{"key":"value","n":42}`),
		Signature:   "deadbeef",
		Timestamp:   eventlog.HLC{Wall: 1700000000123456789, Logical: 11},
	}
	if _, err := s.Append(ctx, r); err != nil {
		t.Fatalf("Append: %v", err)
	}
	tail, err := s.Since(ctx, "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(tail) != 1 {
		t.Fatalf("len: %d", len(tail))
	}
	got := tail[0]
	if got.ID != r.ID || got.Type != r.Type || got.Version != r.Version ||
		got.Actor != r.Actor || got.WorkspaceID != r.WorkspaceID ||
		got.Signature != r.Signature {
		t.Fatalf("scalar fields: got=%+v want=%+v", got, r)
	}
	if got.Timestamp != r.Timestamp {
		t.Fatalf("HLC: got=%+v want=%+v", got.Timestamp, r.Timestamp)
	}
	if string(got.Payload) == "" {
		t.Fatalf("payload empty after roundtrip")
	}
}

func TestPostgresStoreSurvivesPoolReopen(t *testing.T) {
	t.Parallel()

	s, scopedDSN := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = s.Append(ctx, mkRec(id))
	}
	s.Close()

	// New pool against the same scoped DSN — proves persistence
	// across pool lifetimes within the per-test schema.
	ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s2, err := NewPostgresStore(ctx2, scopedDSN)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	t.Cleanup(s2.Close)
	// Use the freshly reopened store to resolve the default org
	// id and re-stamp ctx2 — the org id isn't carried across
	// pools.
	ctx2 = defaultOrgCtx(t, s2)
	tail, err := s2.Since(ctx2, "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(tail) != 3 {
		t.Fatalf("len after reopen: got %d want 3", len(tail))
	}
}

func TestPostgresStoreAppendBatchHappyPath(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	in := []eventlog.Record{
		mkRec("b1"), mkRec("b2"), mkRec("b3"),
	}
	added, err := s.AppendBatch(ctx, in)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("added: got %d want 3 (%v)", len(added), added)
	}
	// Per-record insertion-order isn't part of the contract for
	// the returned ids — the multi-row INSERT may RETURNING in
	// any order — but the set must match.
	gotSet := map[string]bool{}
	for _, id := range added {
		gotSet[id] = true
	}
	for _, want := range []string{"b1", "b2", "b3"} {
		if !gotSet[want] {
			t.Errorf("added missing %q (got=%v)", want, added)
		}
	}
	n, _ := s.Len(ctx)
	if n != 3 {
		t.Fatalf("len after batch: got %d want 3", n)
	}
	tail, _ := s.Since(ctx, "")
	if len(tail) != 3 {
		t.Fatalf("Since after batch: got %d want 3", len(tail))
	}
}

func TestPostgresStoreAppendBatchIsIdempotent(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	in := []eventlog.Record{mkRec("x"), mkRec("y"), mkRec("z")}
	if _, err := s.AppendBatch(ctx, in); err != nil {
		t.Fatalf("first AppendBatch: %v", err)
	}
	// Re-send: every id is a duplicate -> no fresh inserts.
	added, err := s.AppendBatch(ctx, in)
	if err != nil {
		t.Fatalf("re-send AppendBatch: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("re-send should add zero, got %v", added)
	}
	n, _ := s.Len(ctx)
	if n != 3 {
		t.Fatalf("len after re-send: got %d want 3", n)
	}
}

func TestPostgresStoreAppendBatchMixedDuplicates(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	if _, err := s.AppendBatch(ctx, []eventlog.Record{mkRec("a"), mkRec("b")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Batch with one fresh (c) + two duplicates (a, b).
	added, err := s.AppendBatch(ctx, []eventlog.Record{mkRec("a"), mkRec("c"), mkRec("b")})
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if len(added) != 1 || added[0] != "c" {
		t.Fatalf("added: got %v want [c]", added)
	}
	n, _ := s.Len(ctx)
	if n != 3 {
		t.Fatalf("len: got %d want 3", n)
	}
}

// TestPostgresStoreAppendBatchBindsDistinctWorkspaces confirms
// the workspace-binding upsert in AppendBatch fires once per
// distinct workspace id, not per-record — fresh workspaces
// referenced across multiple events all land bound to the
// caller's org (first-push-wins, sync.ORG.6-note).
func TestPostgresStoreAppendBatchBindsDistinctWorkspaces(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	a := mkRec("e-1")
	a.WorkspaceID = "ws-alpha"
	b := mkRec("e-2")
	b.WorkspaceID = "ws-alpha"
	c := mkRec("e-3")
	c.WorkspaceID = "ws-beta"
	if _, err := s.AppendBatch(ctx, []eventlog.Record{a, b, c}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	// Both workspaces are bound to the default org now.
	for _, ws := range []string{"ws-alpha", "ws-beta"} {
		orgID, ok, err := s.WorkspaceOrg(context.Background(), ws)
		if err != nil {
			t.Fatalf("WorkspaceOrg(%s): %v", ws, err)
		}
		if !ok {
			t.Fatalf("workspace %s not bound", ws)
		}
		if orgID == "" {
			t.Fatalf("workspace %s bound to empty org", ws)
		}
	}
}

func TestPostgresStoreAppendBatchEmptyIsNoop(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := defaultOrgCtx(t, s)
	added, err := s.AppendBatch(ctx, nil)
	if err != nil {
		t.Fatalf("AppendBatch(nil): %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("nil input should add nothing: %v", added)
	}
	n, _ := s.Len(ctx)
	if n != 0 {
		t.Fatalf("len after nil batch: got %d want 0", n)
	}
}
