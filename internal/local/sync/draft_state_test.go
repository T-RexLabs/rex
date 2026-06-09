package sync

import (
	"os"
	"path/filepath"
	"testing"
)

// The central-backed draft-state tests (push/pull conflict + rebase
// flag behaviour against a live server) live in
// draft_state_central_test.go behind the `central_e2e` build tag,
// since they need the parked central node. The pure-local TOML
// round-trip below stays in the default suite.

// TestWatermarkTOMLRoundTripWithFlags belt-and-braces test that the
// new fields serialize cleanly through the TOML round-trip.
func TestWatermarkTOMLRoundTripWithFlags(t *testing.T) {
	t.Parallel()

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex", "drafts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := Watermark{
		Remote:           "origin",
		LastAckedEventID: "ev-9",
		NeedsRebase:      true,
		LastConflictHead: "ev-42",
	}
	if err := SaveWatermark(wsRoot, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadWatermark(wsRoot, "origin")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.NeedsRebase != true || got.LastConflictHead != "ev-42" {
		t.Fatalf("round-trip lost flags: %+v", got)
	}

	// Compactness: flags omitempty so a clean watermark file does
	// not carry zero-value noise.
	clean := Watermark{Remote: "origin2", LastAckedEventID: "ev-1"}
	if err := SaveWatermark(wsRoot, clean); err != nil {
		t.Fatalf("Save clean: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(wsRoot, ".rex", "drafts", "origin2.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if contains(string(body), "needs_rebase") {
		t.Errorf("clean watermark leaked needs_rebase: %s", body)
	}
	if contains(string(body), "last_conflict_head") {
		t.Errorf("clean watermark leaked last_conflict_head: %s", body)
	}
}

// contains is a tiny shim so the test reads naturally; strings.Contains
// would do but it pulls in another import.
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
