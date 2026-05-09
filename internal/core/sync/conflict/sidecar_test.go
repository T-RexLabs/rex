package conflict

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSidecarRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "specs", "sync.yaml.conflict")
	original := Sidecar{
		Entity:         "specs/sync.yaml",
		Remote:         "primary",
		BaseRevision:   "rev-base",
		LocalRevision:  "rev-local",
		RemoteRevision: "rev-remote",
		CreatedAt:      time.Now().UTC().Round(time.Second),
		Hunks: []Hunk{
			{
				BaseStart: 3, BaseEnd: 5,
				LocalStart: 3, LocalEnd: 5,
				RemoteStart: 3, RemoteEnd: 5,
				BaseLines:   []string{"old"},
				LocalLines:  []string{"local-edit"},
				RemoteLines: []string{"remote-edit"},
			},
		},
	}
	if err := Write(dest, original); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(dest)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Entity != original.Entity ||
		got.Remote != original.Remote ||
		got.BaseRevision != original.BaseRevision ||
		got.LocalRevision != original.LocalRevision ||
		got.RemoteRevision != original.RemoteRevision {
		t.Fatalf("scalar mismatch: %+v vs %+v", got, original)
	}
	if len(got.Hunks) != 1 || got.Hunks[0].LocalLines[0] != "local-edit" {
		t.Fatalf("hunks: %+v", got.Hunks)
	}
}

func TestSidecarReadMissingReturnsNotExist(t *testing.T) {
	t.Parallel()

	_, err := Read(filepath.Join(t.TempDir(), "absent.conflict"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err: got %v want fs.ErrNotExist", err)
	}
}

func TestSidecarClearMissingIsNoop(t *testing.T) {
	t.Parallel()

	if err := Clear(filepath.Join(t.TempDir(), "absent.conflict")); err != nil {
		t.Fatalf("Clear: %v", err)
	}
}

func TestExistsMatchesCanonicalPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	entity := filepath.Join(dir, "workspace.yaml")
	if err := os.WriteFile(SidecarPathFor(entity), []byte(`entity = "workspace.yaml"`+"\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := Exists(entity)
	if err != nil || !got {
		t.Fatalf("Exists: got=%v err=%v", got, err)
	}
}

// TestIsConflictedDetectsMarkers covers the case where the sidecar
// has been removed but the user has not edited the merge markers
// out of the file. Per sync.GIT.3 the entity is still considered
// conflicted in that case.
func TestIsConflictedDetectsMarkers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	entity := filepath.Join(dir, "specs", "x.yaml")
	if err := os.MkdirAll(filepath.Dir(entity), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "before\n<<<<<<< local\nlocal\n=======\nremote\n>>>>>>> remote\nafter\n"
	if err := os.WriteFile(entity, []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := IsConflicted(entity)
	if err != nil || !got {
		t.Fatalf("IsConflicted: got=%v err=%v", got, err)
	}
}

func TestIsConflictedFalseForCleanFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	entity := filepath.Join(dir, "workspace.yaml")
	if err := os.WriteFile(entity, []byte("name: ok\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := IsConflicted(entity)
	if err != nil || got {
		t.Fatalf("IsConflicted: got=%v err=%v", got, err)
	}
}

func TestHasMarkersIgnoresInlineMatches(t *testing.T) {
	t.Parallel()

	// A line that contains "=======" mid-line should not be
	// treated as a marker (the marker rule is line-anchored).
	body := []byte("description: |\n  the divider is =======\n")
	if HasMarkers(body) {
		t.Fatalf("inline ======= should not match a marker line: %q", body)
	}
}

func TestHasMarkersDetectsLeadingMarker(t *testing.T) {
	t.Parallel()

	body := []byte("<<<<<<< local\nx\n=======\ny\n>>>>>>> remote\n")
	if !HasMarkers(body) {
		t.Fatalf("expected markers detected")
	}
}
