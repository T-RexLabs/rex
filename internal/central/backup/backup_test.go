package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pgDSN returns the test Postgres DSN from REX_PG_TEST_DSN.
// Tests in this package skip when it's unset so go test ./...
// works without Docker; CI sets it via the workflow's
// services: block.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("REX_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("REX_PG_TEST_DSN unset; skipping backup test (needs real pg + pg_dump)")
	}
	return dsn
}

// requirePgTools skips the test when pg_dump or pg_restore are
// missing from PATH. Local devs without postgresql-client
// installed shouldn't see hard failures from this package.
func requirePgTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
	}
}

func TestRunWritesPgDumpFile(t *testing.T) {
	t.Parallel()
	dsn := pgDSN(t)
	requirePgTools(t)

	dir := t.TempDir()
	path, took, err := Run(context.Background(), Options{DSN: dsn, Dir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if took <= 0 {
		t.Errorf("zero duration: %s", took)
	}
	if !strings.HasPrefix(filepath.Base(path), "rex-central-") {
		t.Errorf("unexpected filename: %s", path)
	}
	if !strings.HasSuffix(path, ".dump") {
		t.Errorf("expected .dump suffix: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("dump is empty")
	}
	// First five bytes must be the PGDMP magic. This is what
	// Validate checks; if Run produced a file Validate would
	// reject, we have a real problem.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	head := make([]byte, len(pgdumpMagic))
	if _, err := f.Read(head); err != nil {
		t.Fatalf("read head: %v", err)
	}
	if string(head) != "PGDMP" {
		t.Errorf("magic: got %q want PGDMP", head)
	}
}

func TestRunRejectsMissingDir(t *testing.T) {
	t.Parallel()
	if _, _, err := Run(context.Background(), Options{DSN: "x", Dir: ""}); err == nil {
		t.Fatal("expected error when Dir is empty")
	}
	if _, _, err := Run(context.Background(), Options{DSN: "", Dir: "/tmp"}); err == nil {
		t.Fatal("expected error when DSN is empty")
	}
}

func TestValidateRejectsNonDump(t *testing.T) {
	t.Parallel()
	requirePgTools(t)

	dir := t.TempDir()
	bogus := filepath.Join(dir, "not-a-dump.dump")
	if err := os.WriteFile(bogus, []byte("hello, world"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Validate(context.Background(), bogus); err == nil {
		t.Fatal("expected Validate to reject non-PGDMP file")
	}
}

func TestValidateAcceptsRealDump(t *testing.T) {
	t.Parallel()
	dsn := pgDSN(t)
	requirePgTools(t)

	dir := t.TempDir()
	path, _, err := Run(context.Background(), Options{DSN: dsn, Dir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := Validate(context.Background(), path); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	dsn := pgDSN(t)
	requirePgTools(t)

	// Set up a fresh schema so the round trip doesn't touch
	// other tests' data. Use schema-qualified names rather than
	// search_path on the DSN — psql's URI parser rejects
	// search_path as a query param (only pgx accepts it).
	schema := "rextest_backup_roundtrip"
	if err := psqlExec(dsn, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _ = psqlExec(dsn, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	// Seed a row in the schema.
	if err := psqlExec(dsn,
		`CREATE TABLE `+schema+`.t (id int PRIMARY KEY, msg text);
		 INSERT INTO `+schema+`.t VALUES (1, 'before-restore');`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Backup. pg_dump takes search_path on the DSN; PostgresStore
	// already proves that side works. For the dump itself we
	// scope by --schema=<name>.
	dir := t.TempDir()
	path, _, err := dumpSchema(context.Background(), dsn, schema, dir)
	if err != nil {
		t.Fatalf("dumpSchema: %v", err)
	}

	// Mutate after the backup so we can prove restore reverts.
	if err := psqlExec(dsn,
		`UPDATE `+schema+`.t SET msg = 'after-restore' WHERE id = 1`,
	); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	// Restore. --clean drops the existing schema bits so the
	// dump's version wins.
	if err := Restore(context.Background(), dsn, path); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// The restored row should be the original 'before-restore'.
	got, err := psqlScalar(dsn, `SELECT msg FROM `+schema+`.t WHERE id = 1`)
	if err != nil {
		t.Fatalf("scalar: %v", err)
	}
	if got != "before-restore" {
		t.Errorf("after restore msg = %q, want %q", got, "before-restore")
	}
}

// dumpSchema is a test-only variant of Run that scopes pg_dump
// to a single schema via --schema=<name>. The production Run
// dumps the whole DB; this lets the round-trip test isolate
// from any other state in the shared test database.
func dumpSchema(ctx context.Context, dsn, schema, dir string) (string, time.Duration, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(dir, "rex-central-"+stamp+".dump")
	start := time.Now()
	cmd := exec.CommandContext(ctx, "pg_dump",
		"-Fc", "--no-owner", "--no-privileges",
		"--schema="+schema,
		"--file="+path,
		dsn,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", time.Since(start), err
	}
	return path, time.Since(start), nil
}

func TestPruneKeepsLatestN(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, ts := range []string{
		"20260501T000000Z",
		"20260502T000000Z",
		"20260503T000000Z",
		"20260504T000000Z",
		"20260505T000000Z",
	} {
		if err := os.WriteFile(
			filepath.Join(dir, "rex-central-"+ts+".dump"),
			[]byte("placeholder"), 0o600,
		); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	pruned, err := prune(dir, 3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned: got %d want 2", pruned)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("remaining: got %d want 3", len(entries))
	}
	// Newest three remain.
	for _, ts := range []string{"20260503", "20260504", "20260505"} {
		found := false
		for _, e := range entries {
			if strings.Contains(e.Name(), ts) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s to remain", ts)
		}
	}
}

func TestPruneIgnoresUnrelatedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{
		"rex-central-20260501T000000Z.dump",
		"some-other-file.txt",
		"backup.tar.gz",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	if pruned, err := prune(dir, 0); err != nil || pruned != 1 {
		t.Errorf("prune(0): got %d/%v want 1/<nil>", pruned, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("expected 2 unrelated files left, got %d", len(entries))
	}
}

func TestScheduleStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	dsn := pgDSN(t)
	requirePgTools(t)

	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	done := make(chan struct{})
	go func() {
		Schedule(ctx, Options{
			DSN:     dsn,
			Dir:     dir,
			Cadence: 200 * time.Millisecond,
		})
		close(done)
	}()
	// Wait long enough for one tick + one pg_dump round trip.
	// pg_dump on an empty database is normally <300ms; 1.5s
	// gives generous headroom on a busy CI runner.
	time.Sleep(1500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Schedule did not exit on cancel")
	}
	// The scheduler should have written at least one dump.
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("scheduler wrote zero dumps in 1.5s")
	}
}

func TestScheduleNoOpWhenCadenceZero(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Schedule(ctx, Options{DSN: "x", Dir: t.TempDir(), Cadence: 0})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Schedule should have returned immediately when Cadence=0")
	}
}

// psqlExec runs a SQL string via psql against dsn. Used by the
// round-trip test to seed data and inspect it after restore.
func psqlExec(dsn, sql string) error {
	cmd := exec.Command("psql", dsn, "-v", "ON_ERROR_STOP=1", "-c", sql)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// psqlScalar runs a single-value SELECT via psql --tuples-only
// and returns the trimmed string result.
func psqlScalar(dsn, sql string) (string, error) {
	out, err := exec.Command("psql", dsn,
		"-tA", "-v", "ON_ERROR_STOP=1", "-c", sql,
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
