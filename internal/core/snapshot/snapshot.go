package snapshot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// SnapshotsDirName is the per-workspace directory holding snapshot
// children (storage.WS.2.10).
const SnapshotsDirName = "snapshots"

// metaDirName mirrors the workspace .rex/ directory we capture
// from. Inlined to avoid coupling the snapshot package to the cli
// package.
const metaDirName = ".rex"

// CapturedComponents are the top-level entries inside .rex/ that a
// v1 snapshot copies. Listed here so it's a single source of truth
// for create + restore + manifest.captured_components.
//
// Excluded by design:
//
//   - events.log: the canonical event store; restoring it would
//     conflict with storage.SNAP.4's "preserves the event log
//     unchanged".
//   - transcripts/: event-sourced like events.log; recoverable by
//     replay.
//   - drafts/: per-remote watermark files (local-only state); not
//     meaningful to restore across edits.
//   - snapshots/: snapshots-of-snapshots break the recursion
//     invariant.
//   - index.sqlite, hook-log/, migrations-backup/: derived or
//     ephemeral.
var CapturedComponents = []string{
	"workspace.yaml",
	"specs",
	"schedules",
	"templates",
	"hooks",
}

// CreateOptions configure a Create call.
type CreateOptions struct {
	// WorkspaceRoot is the workspace directory (the parent of
	// .rex/).
	WorkspaceRoot string
	// Now returns the snapshot's creation timestamp. Defaults to
	// time.Now when nil.
	Now func() time.Time
	// Clock mints the snapshot id (an HLC string). Defaults to a
	// fresh wall-clock-backed clock.
	Clock *eventlog.Clock
}

// Create builds a new snapshot of opts.WorkspaceRoot under
// .rex/snapshots/<snapshot-id>/. The operation is atomic: the
// snapshot is assembled in a temp directory, then renamed into
// place. A failed create leaves no partial directory under
// snapshots/.
func Create(opts CreateOptions) (Manifest, error) {
	if opts.WorkspaceRoot == "" {
		return Manifest{}, errors.New("snapshot: WorkspaceRoot is required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	clock := opts.Clock
	if clock == nil {
		clock = eventlog.NewClock()
	}

	snapID := clock.Now().String()
	createdAt := now().UTC()

	rexDir := filepath.Join(opts.WorkspaceRoot, metaDirName)
	snapsDir := filepath.Join(rexDir, SnapshotsDirName)
	if err := os.MkdirAll(snapsDir, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("snapshot: mkdir %s: %w", snapsDir, err)
	}

	tmpDir, err := os.MkdirTemp(snapsDir, ".tmp-")
	if err != nil {
		return Manifest{}, fmt.Errorf("snapshot: tempdir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	captured := make([]string, 0, len(CapturedComponents))
	for _, name := range CapturedComponents {
		src := filepath.Join(rexDir, name)
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Not present in this workspace; skip.
				continue
			}
			cleanup()
			return Manifest{}, fmt.Errorf("snapshot: stat %s: %w", src, err)
		}
		dst := filepath.Join(tmpDir, name)
		if err := copyTree(src, dst); err != nil {
			cleanup()
			return Manifest{}, err
		}
		captured = append(captured, name)
	}

	head, err := readEventLogHead(filepath.Join(rexDir, "events.log"))
	if err != nil {
		cleanup()
		return Manifest{}, err
	}
	wsID, _ := readWorkspaceID(filepath.Join(rexDir, "workspace.yaml"))

	m := Manifest{
		SnapshotID:         snapID,
		CreatedAt:          createdAt,
		LastEventID:        head,
		SourceWorkspaceID:  wsID,
		CapturedComponents: captured,
	}
	if err := writeManifest(filepath.Join(tmpDir, ManifestFileName), m); err != nil {
		cleanup()
		return Manifest{}, err
	}

	final := filepath.Join(snapsDir, snapID)
	if err := os.Rename(tmpDir, final); err != nil {
		cleanup()
		return Manifest{}, fmt.Errorf("snapshot: rename %s -> %s: %w", tmpDir, final, err)
	}
	return m, nil
}

// List returns the manifests of every snapshot under
// .rex/snapshots/<id>/, sorted by CreatedAt descending. Snapshots
// whose manifests fail to parse are skipped.
func List(workspaceRoot string) ([]Manifest, error) {
	snapsDir := filepath.Join(workspaceRoot, metaDirName, SnapshotsDirName)
	entries, err := os.ReadDir(snapsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("snapshot: read %s: %w", snapsDir, err)
	}
	out := make([]Manifest, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip in-flight tempdirs left over from a failed Create.
		if len(e.Name()) >= 5 && e.Name()[:5] == ".tmp-" {
			continue
		}
		manifestPath := filepath.Join(snapsDir, e.Name(), ManifestFileName)
		m, err := readManifest(manifestPath)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// Restore replays the snapshot's git-merged components back into
// the workspace, overwriting whatever is currently on disk for those
// paths. The event log (events.log + transcripts/) is left
// untouched (storage.SNAP.4).
//
// Restore is best-effort, not transactional: a crash mid-restore can
// leave the workspace with some components from the snapshot and
// others from the live state. v1 callers that care about atomicity
// should snapshot first, then restore — the snapshot taken
// immediately before restore captures the live state and gives the
// user a manual rollback path.
func Restore(workspaceRoot, snapshotID string) (Manifest, error) {
	rexDir := filepath.Join(workspaceRoot, metaDirName)
	snapDir := filepath.Join(rexDir, SnapshotsDirName, snapshotID)

	m, err := readManifest(filepath.Join(snapDir, ManifestFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, fmt.Errorf("snapshot: %q not found in %s", snapshotID, snapDir)
		}
		return Manifest{}, err
	}
	if m.SnapshotID != snapshotID {
		return Manifest{}, fmt.Errorf("snapshot: directory %q has manifest claiming id %q",
			snapshotID, m.SnapshotID)
	}

	// Iterate captured_components rather than CapturedComponents so
	// snapshots from older Rex versions (with a smaller set) restore
	// only what they actually captured.
	for _, name := range m.CapturedComponents {
		src := filepath.Join(snapDir, name)
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Manifest{}, fmt.Errorf("snapshot: stat %s: %w", src, err)
		}
		dst := filepath.Join(rexDir, name)
		// Replace semantics: remove what's there before copying so
		// restoring a snapshot that lacks an entry effectively
		// clears it from the workspace. We only remove things in
		// the captured set; other entries (events.log, drafts/,
		// etc.) are untouched.
		if err := os.RemoveAll(dst); err != nil {
			return Manifest{}, fmt.Errorf("snapshot: remove %s: %w", dst, err)
		}
		if err := copyTree(src, dst); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// Prune deletes snapshots according to policy. Returns the IDs of
// the snapshots that were removed and any error encountered. The
// retention defaults from storage.SNAP.5 (last 7 + monthly forever)
// are encoded in DefaultPolicy.
func Prune(workspaceRoot string, policy Policy) ([]string, error) {
	snaps, err := List(workspaceRoot)
	if err != nil {
		return nil, err
	}
	delete := pickDeletes(snaps, policy)
	deleted := make([]string, 0, len(delete))
	for _, id := range delete {
		dir := filepath.Join(workspaceRoot, metaDirName, SnapshotsDirName, id)
		if err := os.RemoveAll(dir); err != nil {
			return deleted, fmt.Errorf("snapshot: prune %s: %w", id, err)
		}
		deleted = append(deleted, id)
	}
	return deleted, nil
}

// Policy describes which snapshots Prune should retain.
type Policy struct {
	// KeepLast retains the N most recent snapshots regardless of
	// age. 0 means "no count-based retention".
	KeepLast int
	// KeepMonthly retains, for each calendar month that any
	// snapshot was created in, the most recent snapshot in that
	// month. Combined with KeepLast: a snapshot retained by either
	// rule survives.
	KeepMonthly bool
}

// DefaultPolicy is storage.SNAP.5's recommended default.
var DefaultPolicy = Policy{KeepLast: 7, KeepMonthly: true}

// pickDeletes returns IDs of snapshots that should be deleted under
// policy. Input is assumed sorted by CreatedAt descending.
func pickDeletes(snaps []Manifest, policy Policy) []string {
	keep := make(map[string]bool, len(snaps))
	seenMonth := make(map[string]struct{})
	for i, s := range snaps {
		if policy.KeepLast > 0 && i < policy.KeepLast {
			keep[s.SnapshotID] = true
		}
		if policy.KeepMonthly {
			month := s.CreatedAt.UTC().Format("2006-01")
			if _, dup := seenMonth[month]; !dup {
				seenMonth[month] = struct{}{}
				keep[s.SnapshotID] = true
			}
		}
	}
	out := make([]string, 0, len(snaps))
	for _, s := range snaps {
		if !keep[s.SnapshotID] {
			out = append(out, s.SnapshotID)
		}
	}
	return out
}

// readEventLogHead returns the id of the latest record in
// events.log, or "" when the file is missing or empty.
func readEventLogHead(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	r, err := eventlog.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	var lastID string
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		lastID = rec.ID
	}
	return lastID, nil
}

// readWorkspaceID extracts metadata.id from workspace.yaml without
// pulling in the cli package's full settings type. Best-effort:
// returns empty string on any error so a snapshot of a partial
// workspace still succeeds.
func readWorkspaceID(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Hand-roll a tiny "id: <value>" lookup to avoid pulling
	// gopkg.in/yaml.v3 into the snapshot package; the field is
	// always at the top level of workspace.yaml and never
	// quoted/multi-line.
	for _, line := range splitLines(body) {
		if len(line) > 4 && line[:4] == "id: " {
			return line[4:], nil
		}
	}
	return "", nil
}

func splitLines(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}
