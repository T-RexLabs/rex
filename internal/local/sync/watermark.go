package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// DraftsDirName is the per-workspace directory that holds remote
// watermark files (storage.WS.2.12).
const DraftsDirName = "drafts"

// Watermark is the on-disk shape of a per-remote watermark file. It
// records the highest event id from events.log that the named remote
// has acknowledged plus when that acknowledgement happened. Events
// past LastAckedEventID are drafts for that remote (sync.DRAFT.1).
//
// The NeedsRebase / LastConflictHead pair captures sync.DRAFT.2's
// "rebase-needed flag": when a push attempt returns 409 with a
// diverging tail, the watermark records the server's head so the next
// `rex status` (and any other read-only surface) can flag the remote
// as needing a rebase without re-issuing a network call. The flag
// clears on the next successful push or pull.
type Watermark struct {
	// Remote is the name of the remote this watermark belongs to.
	// Persisted alongside the rest of the file so a watermark
	// loaded by name always carries its identity.
	Remote string `toml:"remote"`
	// LastAckedEventID is the eventlog.Record.ID the remote
	// confirmed receipt of, or "" when the remote has never been
	// pushed to.
	LastAckedEventID string `toml:"last_acked_event_id"`
	// AckedAt is when the local node last successfully pushed to
	// this remote. Persisted as RFC3339.
	AckedAt time.Time `toml:"acked_at"`
	// NeedsRebase is true when the most recent push attempt against
	// this remote returned 409. Cleared on the next successful push
	// or pull. Drives sync.DRAFT.2's status display.
	NeedsRebase bool `toml:"needs_rebase,omitempty"`
	// LastConflictHead carries the server head reported on the most
	// recent push conflict. Empty when the watermark has never
	// observed a conflict, or after the conflict has been resolved.
	// Surfaces in `rex status` so the user knows what the remote
	// thinks the head is without re-issuing a /sync/state call.
	LastConflictHead string `toml:"last_conflict_head,omitempty"`
}

// WatermarkPath returns the canonical file path for a watermark.
func WatermarkPath(workspaceRoot, remote string) string {
	return filepath.Join(workspaceRoot, ".rex", DraftsDirName, remote+".toml")
}

// LoadWatermark reads the watermark for the named remote in the
// given workspace. Returns a zero-valued Watermark + nil error when
// the file does not exist; that is the natural pre-first-push state.
func LoadWatermark(workspaceRoot, remote string) (Watermark, error) {
	if remote == "" {
		return Watermark{}, errors.New("sync: remote name is required")
	}
	path := WatermarkPath(workspaceRoot, remote)
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Watermark{Remote: remote}, nil
		}
		return Watermark{}, fmt.Errorf("sync: read %s: %w", path, err)
	}
	var w Watermark
	if err := toml.Unmarshal(body, &w); err != nil {
		return Watermark{}, fmt.Errorf("sync: decode %s: %w", path, err)
	}
	if w.Remote == "" {
		w.Remote = remote
	}
	return w, nil
}

// SaveWatermark atomically writes the watermark for the named remote.
// Refuses to silently rename watermarks: if w.Remote is set and
// disagrees with the path-derived name, returns an error.
func SaveWatermark(workspaceRoot string, w Watermark) error {
	if w.Remote == "" {
		return errors.New("sync: SaveWatermark requires Remote")
	}
	path := WatermarkPath(workspaceRoot, w.Remote)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sync: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".watermark-*")
	if err != nil {
		return fmt.Errorf("sync: tempfile: %w", err)
	}
	if err := toml.NewEncoder(tmp).Encode(w); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync: encode %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync: close %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync: rename %s: %w", path, err)
	}
	return nil
}
