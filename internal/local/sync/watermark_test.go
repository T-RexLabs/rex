package sync

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadWatermarkMissingReturnsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wm, err := LoadWatermark(dir, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if wm.Remote != "primary" {
		t.Fatalf("remote: got %q want primary", wm.Remote)
	}
	if wm.LastAckedEventID != "" {
		t.Fatalf("last acked: got %q want empty", wm.LastAckedEventID)
	}
	if !wm.AckedAt.IsZero() {
		t.Fatalf("acked_at: got %v want zero", wm.AckedAt)
	}
}

func TestSaveLoadWatermarkRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	want := Watermark{
		Remote:           "primary",
		LastAckedEventID: "1700000000.0",
		AckedAt:          time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
	}
	if err := SaveWatermark(dir, want); err != nil {
		t.Fatalf("SaveWatermark: %v", err)
	}

	got, err := LoadWatermark(dir, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if got.Remote != want.Remote || got.LastAckedEventID != want.LastAckedEventID {
		t.Fatalf("round-trip: got %+v want %+v", got, want)
	}
	if !got.AckedAt.Equal(want.AckedAt) {
		t.Fatalf("acked_at drifted: got %v want %v", got.AckedAt, want.AckedAt)
	}
}

func TestSaveWatermarkRequiresRemote(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := SaveWatermark(dir, Watermark{LastAckedEventID: "x"}); err == nil {
		t.Fatal("expected error when Remote is empty")
	}
}

func TestLoadWatermarkRequiresRemote(t *testing.T) {
	t.Parallel()

	if _, err := LoadWatermark(t.TempDir(), ""); err == nil {
		t.Fatal("expected error when remote is empty")
	}
}

func TestWatermarkPathPlacement(t *testing.T) {
	t.Parallel()

	got := WatermarkPath("/ws", "primary")
	want := filepath.Join("/ws", ".rex", "drafts", "primary.toml")
	if got != want {
		t.Fatalf("path: got %q want %q", got, want)
	}
}

func TestSaveWatermarkAtomicallyOverwrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := Watermark{Remote: "primary", LastAckedEventID: "id-1", AckedAt: time.Now().UTC()}
	second := Watermark{Remote: "primary", LastAckedEventID: "id-2", AckedAt: time.Now().UTC()}
	if err := SaveWatermark(dir, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := SaveWatermark(dir, second); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := LoadWatermark(dir, "primary")
	if err != nil {
		t.Fatalf("LoadWatermark: %v", err)
	}
	if got.LastAckedEventID != "id-2" {
		t.Fatalf("overwrite: got %q want id-2", got.LastAckedEventID)
	}
}

// TestLoadWatermarkUnknownPath ensures an error encountered for any
// reason other than ErrNotExist is wrapped, not silently swallowed.
func TestLoadWatermarkUnknownPath(t *testing.T) {
	t.Parallel()

	// On most systems os.ReadFile on a directory returns an error
	// other than fs.ErrNotExist; use that to exercise the
	// non-not-exist failure path.
	dir := t.TempDir()
	collisionDir := WatermarkPath(dir, "primary")
	if err := makeDirAt(collisionDir); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := LoadWatermark(dir, "primary")
	if err == nil {
		t.Fatal("expected error reading a directory as a TOML file")
	}
	if errors.Is(err, ErrUnknownSince) {
		t.Fatalf("error should not be ErrUnknownSince: %v", err)
	}
}

func makeDirAt(path string) error {
	return mkdirAll(path)
}
