// Package backup implements the scheduled pg_dump and the
// restore-validator that central-node.BACKUP.* requires.
//
// The package shells out to `pg_dump` and `pg_restore` from PATH.
// The bundled rex-central image installs postgresql-client so
// these are available; bare-metal deployments must install
// postgresql-client themselves (documented in deploy/README.md).
//
// File format: PostgreSQL "custom" dump format (-Fc), the
// compact binary layout pg_restore reads. The format embeds a
// magic header ("PGDMP") so /restore can validate before
// applying. Plain SQL dumps would also work but custom format
// supports parallel restore later (BACKUP.3 doesn't require it
// today; not painting into a corner).
package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// pgdumpMagic is the first 5 bytes of every custom-format dump
// produced by pg_dump (-Fc / --format=custom). Used by Validate
// as a cheap sanity check before invoking pg_restore --list.
var pgdumpMagic = []byte("PGDMP")

// Options configure Run + Restore + Schedule.
type Options struct {
	// DSN is the libpq-style connection string pg_dump and
	// pg_restore are pointed at. Same DSN the central uses for
	// its event store; nothing else.
	DSN string

	// Dir is where Run writes dump files. The filename is
	// "rex-central-<RFC3339-utc>.dump"; one file per run.
	Dir string

	// Cadence is the schedule period. <=0 disables the
	// scheduler (Schedule returns immediately when set so).
	Cadence time.Duration

	// Retention caps the number of dumps in Dir; the scheduler
	// deletes older files after a successful run. 0 means keep
	// every dump (an external retention policy applies).
	Retention int

	// Logger is the structured logger the scheduler writes
	// progress to. Nil disables logging.
	Logger *slog.Logger

	// Now is injectable for deterministic tests of the
	// retention sweep + filename layout. Defaults to time.Now.
	Now func() time.Time
}

// Run executes a single pg_dump pass against opts.DSN, writing
// the result to opts.Dir/<timestamped>.dump. Returns the path of
// the written dump, the duration of the dump, and any error.
//
// Errors include: pg_dump not on PATH, dir not writable, DSN
// rejected by pg_dump. Errors are surfaced verbatim to the
// caller (the scheduler logs + retries on the next tick).
func Run(ctx context.Context, opts Options) (path string, took time.Duration, err error) {
	if opts.DSN == "" {
		return "", 0, errors.New("backup: DSN is required")
	}
	if opts.Dir == "" {
		return "", 0, errors.New("backup: Dir is required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(opts.Dir, 0o750); err != nil {
		return "", 0, fmt.Errorf("backup: mkdir %s: %w", opts.Dir, err)
	}

	stamp := now().UTC().Format("20060102T150405Z")
	path = filepath.Join(opts.Dir, "rex-central-"+stamp+".dump")

	start := time.Now()
	// pg_dump flags:
	//   -Fc                 custom format (compact binary)
	//   --no-owner          don't dump ownership (portable across
	//                       deployments)
	//   --no-privileges     don't dump GRANT statements
	//   --file=<path>       write here, not stdout (avoids piping
	//                       from Go and lets pg_dump use atomic
	//                       rename internally)
	cmd := exec.CommandContext(ctx, "pg_dump",
		"-Fc",
		"--no-owner",
		"--no-privileges",
		"--file="+path,
		opts.DSN,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Best-effort cleanup of any partial file pg_dump
		// might have left behind.
		_ = os.Remove(path)
		return "", time.Since(start), fmt.Errorf("backup: pg_dump: %w (stderr=%q)", err, strings.TrimSpace(stderr.String()))
	}
	took = time.Since(start)

	if opts.Retention > 0 {
		if pruned, perr := prune(opts.Dir, opts.Retention); perr != nil && opts.Logger != nil {
			opts.Logger.Warn("backup: retention prune failed",
				"op", "backup.prune",
				"err", perr,
				"pruned", pruned,
			)
		}
	}
	return path, took, nil
}

// Validate checks that path looks like a pg_dump custom-format
// file and that pg_restore --list parses it without error.
// Returns nil on success — Restore can safely proceed. Does NOT
// apply the dump.
func Validate(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer f.Close()
	head := make([]byte, len(pgdumpMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("backup: read header %s: %w", path, err)
	}
	if !bytes.Equal(head, pgdumpMagic) {
		return fmt.Errorf("backup: %s does not look like a pg_dump custom-format file (no PGDMP magic)", path)
	}
	// Ask pg_restore to list the TOC without applying anything.
	// If pg_restore can't even list, the file is corrupt or
	// unsupported and we shouldn't proceed to apply.
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("backup: pg_restore --list %s: %w (stderr=%q)", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Restore applies path to the database at dsn using
// pg_restore. The dump is validated first via Validate; a
// validation failure is surfaced before any destructive work.
//
// The flags:
//   --clean            drop existing objects before recreating
//   --if-exists        avoid noisy errors when --clean has
//                      nothing to drop (fresh DB)
//   --no-owner         match the no-owner dump
//   --no-privileges    match the no-privileges dump
//   -d <dsn>           destination database
//
// pg_restore writes its own progress to stderr; Restore captures
// it and returns it as part of the error message on failure so
// operators don't have to re-run with -v to see what broke.
func Restore(ctx context.Context, dsn, path string) error {
	if dsn == "" {
		return errors.New("backup: DSN is required")
	}
	if err := Validate(ctx, path); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "pg_restore",
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-privileges",
		"-d", dsn,
		path,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("backup: pg_restore: %w (stderr=%q)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Schedule starts a background loop that calls Run on every
// opts.Cadence tick until ctx cancels. Designed to be invoked
// via `go schedule(ctx, opts)` from the rex-central serve
// command after the HTTP listener starts.
//
// A failed Run does NOT terminate the loop — the next tick
// retries. Each outcome is logged via opts.Logger when set.
//
// Returns immediately when Cadence <= 0 or Dir is empty (the
// scheduler is opt-in: an unconfigured deployment doesn't run
// backups).
func Schedule(ctx context.Context, opts Options) {
	if opts.Cadence <= 0 || opts.Dir == "" {
		return
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	logger.Info("backup scheduler starting",
		"op", "backup.schedule.start",
		"dir", opts.Dir,
		"cadence", opts.Cadence.String(),
		"retention", opts.Retention,
	)
	ticker := time.NewTicker(opts.Cadence)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("backup scheduler stopping", "op", "backup.schedule.stop")
			return
		case <-ticker.C:
			path, took, err := Run(ctx, opts)
			if err != nil {
				logger.Error("backup run failed",
					"op", "backup.run",
					"err", err.Error(),
				)
				continue
			}
			logger.Info("backup written",
				"op", "backup.run",
				"path", path,
				"duration", took.String(),
			)
		}
	}
}

// prune deletes oldest dump files in dir until at most keep
// remain. Uses lex order on filename, which is also chronological
// because filenames embed an RFC3339-shaped timestamp.
// Returns the number of files removed.
func prune(dir string, keep int) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var dumps []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "rex-central-") || !strings.HasSuffix(name, ".dump") {
			continue
		}
		dumps = append(dumps, name)
	}
	sort.Strings(dumps) // oldest first
	if len(dumps) <= keep {
		return 0, nil
	}
	pruned := 0
	for _, n := range dumps[:len(dumps)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return pruned, fmt.Errorf("backup: prune %s: %w", n, err)
		}
		pruned++
	}
	return pruned, nil
}
