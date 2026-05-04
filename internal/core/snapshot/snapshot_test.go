package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initWorkspace builds a TempDir workspace with a populated .rex/
// tree mimicking what `rex workspace init` produces, plus extra
// files in each captured component so we can verify they round-trip.
func initWorkspace(t *testing.T, id string) string {
	t.Helper()
	root := t.TempDir()
	rex := filepath.Join(root, ".rex")
	for _, sub := range []string{"specs", "schedules", "templates", "hooks"} {
		if err := os.MkdirAll(filepath.Join(rex, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	settings := []byte("id: " + id + "\nname: " + id + "\nstate: active\ncreated_at: 2026-05-04T00:00:00Z\n")
	if err := os.WriteFile(filepath.Join(rex, "workspace.yaml"), settings, 0o644); err != nil {
		t.Fatalf("workspace.yaml: %v", err)
	}
	for _, f := range []string{
		filepath.Join("specs", "alpha.yaml"),
		filepath.Join("schedules", "nightly.yaml"),
		filepath.Join("templates", "default.yaml"),
		filepath.Join("hooks", "post-spec-edit"),
	} {
		path := filepath.Join(rex, f)
		if err := os.WriteFile(path, []byte("payload-of-"+f), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}
	// Things that must NOT be captured. events.log is left empty
	// here so readEventLogHead returns "" cleanly; the marker for
	// "events.log was modified post-restore" tests inject content
	// after their snapshot has been taken.
	if err := os.WriteFile(filepath.Join(rex, "events.log"), nil, 0o600); err != nil {
		t.Fatalf("seed events.log: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rex, "drafts"), 0o755); err != nil {
		t.Fatalf("seed drafts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rex, "drafts", "primary.toml"), []byte("local-only"), 0o644); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	return root
}

func TestCreateProducesSnapshotWithCapturedComponents(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "demo")
	now := func() time.Time { return time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC) }
	m, err := Create(CreateOptions{WorkspaceRoot: root, Now: now})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.SnapshotID == "" {
		t.Fatal("SnapshotID should be set")
	}
	if m.SourceWorkspaceID != "demo" {
		t.Fatalf("source workspace id: got %q", m.SourceWorkspaceID)
	}
	if !m.CreatedAt.Equal(now()) {
		t.Fatalf("created_at: got %v want %v", m.CreatedAt, now())
	}

	dir := filepath.Join(root, ".rex", "snapshots", m.SnapshotID)
	for _, want := range []string{
		"manifest.toml",
		"workspace.yaml",
		filepath.Join("specs", "alpha.yaml"),
		filepath.Join("schedules", "nightly.yaml"),
		filepath.Join("templates", "default.yaml"),
		filepath.Join("hooks", "post-spec-edit"),
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s in snapshot: %v", want, err)
		}
	}

	// events.log and drafts/ are explicitly NOT captured.
	for _, banned := range []string{"events.log", "drafts"} {
		if _, err := os.Stat(filepath.Join(dir, banned)); err == nil {
			t.Errorf("snapshot leaked %s", banned)
		}
	}
}

func TestCreateAtomicViaTempThenRename(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "demo")
	if _, err := Create(CreateOptions{WorkspaceRoot: root}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No leftover .tmp- entries under snapshots/.
	entries, err := os.ReadDir(filepath.Join(root, ".rex", "snapshots"))
	if err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover tempdir: %s", e.Name())
		}
	}
}

func TestListReturnsManifestsByCreatedAtDesc(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "list-test")
	for i, ts := range []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	} {
		ts := ts
		_, err := Create(CreateOptions{
			WorkspaceRoot: root,
			Now:           func() time.Time { return ts },
		})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	got, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3", len(got))
	}
	if !(got[0].CreatedAt.After(got[1].CreatedAt) && got[1].CreatedAt.After(got[2].CreatedAt)) {
		t.Fatalf("ordering: %+v", got)
	}
}

func TestListMissingDirReturnsEmpty(t *testing.T) {
	t.Parallel()

	got, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty: %v", got)
	}
}

func TestRestoreReplacesGitMergedContent(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "restore")
	m, err := Create(CreateOptions{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Edit a spec and the workspace.yaml after the snapshot.
	specPath := filepath.Join(root, ".rex", "specs", "alpha.yaml")
	if err := os.WriteFile(specPath, []byte("post-snapshot-edit"), 0o644); err != nil {
		t.Fatalf("edit spec: %v", err)
	}
	wsPath := filepath.Join(root, ".rex", "workspace.yaml")
	if err := os.WriteFile(wsPath, []byte("modified workspace"), 0o644); err != nil {
		t.Fatalf("edit workspace: %v", err)
	}

	// Add a brand-new spec that didn't exist when the snapshot was taken.
	newSpec := filepath.Join(root, ".rex", "specs", "added-after.yaml")
	if err := os.WriteFile(newSpec, []byte("brand new"), 0o644); err != nil {
		t.Fatalf("add spec: %v", err)
	}

	// Touch the events.log to confirm restore leaves it alone.
	logPath := filepath.Join(root, ".rex", "events.log")
	if err := os.WriteFile(logPath, []byte("post-snapshot-events"), 0o600); err != nil {
		t.Fatalf("edit events.log: %v", err)
	}

	if _, err := Restore(root, m.SnapshotID); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	gotSpec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if string(gotSpec) != "payload-of-specs/alpha.yaml" {
		t.Fatalf("spec not restored: %q", gotSpec)
	}
	if _, err := os.Stat(newSpec); err == nil {
		t.Fatal("post-snapshot-added spec should be removed by restore")
	}
	gotWS, _ := os.ReadFile(wsPath)
	if !strings.Contains(string(gotWS), "id: restore") {
		t.Fatalf("workspace.yaml not restored: %q", gotWS)
	}

	// events.log was not part of the snapshot; restore must NOT
	// overwrite it.
	gotLog, _ := os.ReadFile(logPath)
	if string(gotLog) != "post-snapshot-events" {
		t.Fatalf("events.log was modified by Restore: %q", gotLog)
	}
}

func TestRestoreMissingSnapshotErrors(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "restore")
	_, err := Restore(root, "no-such-snapshot")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestPruneKeepsLastN(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "prune")
	// Create 10 snapshots spaced one minute apart.
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		if _, err := Create(CreateOptions{
			WorkspaceRoot: root,
			Now:           func() time.Time { return ts },
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	// All in the same month, so monthly retention keeps the
	// newest. KeepLast=3 → keep 3 newest. The newest is also the
	// monthly winner (same set), so total kept = 3.
	deleted, err := Prune(root, Policy{KeepLast: 3, KeepMonthly: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(deleted) != 7 {
		t.Fatalf("deleted: got %d want 7", len(deleted))
	}
	remaining, _ := List(root)
	if len(remaining) != 3 {
		t.Fatalf("remaining: got %d want 3", len(remaining))
	}
}

func TestPruneKeepsMonthlyAcrossMonths(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "prune-monthly")
	// One snapshot per month for 4 months. KeepLast=2 only keeps
	// the 2 newest by count; KeepMonthly keeps one per month, so
	// all 4 should survive.
	for i, ts := range []time.Time{
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
	} {
		ts := ts
		if _, err := Create(CreateOptions{
			WorkspaceRoot: root,
			Now:           func() time.Time { return ts },
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	deleted, err := Prune(root, Policy{KeepLast: 2, KeepMonthly: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("monthly retention should keep all 4, got %d deleted", len(deleted))
	}
}

func TestPruneNoPolicyKeepsAll(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "prune-noop")
	for i := 0; i < 3; i++ {
		ts := time.Date(2026, 5, 1+i, 0, 0, 0, 0, time.UTC)
		if _, err := Create(CreateOptions{
			WorkspaceRoot: root,
			Now:           func() time.Time { return ts },
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	// Empty policy keeps nothing — all snapshots get deleted.
	deleted, err := Prune(root, Policy{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(deleted) != 3 {
		t.Fatalf("deleted: got %d want 3", len(deleted))
	}
}

func TestManifestRoundTrip(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "manifest")
	m, err := Create(CreateOptions{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mPath := filepath.Join(root, ".rex", "snapshots", m.SnapshotID, "manifest.toml")
	loaded, err := readManifest(mPath)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if loaded.SnapshotID != m.SnapshotID {
		t.Fatalf("snapshot id: got %q want %q", loaded.SnapshotID, m.SnapshotID)
	}
	if loaded.SourceWorkspaceID != m.SourceWorkspaceID {
		t.Fatalf("source workspace id drift: %q vs %q", loaded.SourceWorkspaceID, m.SourceWorkspaceID)
	}
	if !loaded.CreatedAt.Equal(m.CreatedAt) {
		t.Fatalf("created_at drift: %v vs %v", loaded.CreatedAt, m.CreatedAt)
	}
	if len(loaded.CapturedComponents) != len(m.CapturedComponents) {
		t.Fatalf("captured components: got %v want %v", loaded.CapturedComponents, m.CapturedComponents)
	}
}

func TestSplitLinesRoundTrip(t *testing.T) {
	t.Parallel()

	got := splitLines([]byte("a\nb\nc"))
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}
