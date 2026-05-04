package snapshot

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// ManifestFileName is the conventional manifest filename inside a
// snapshot directory.
const ManifestFileName = "manifest.toml"

// Manifest is the on-disk shape of a snapshot's metadata file.
// snapshot_id matches the directory name; carrying it inside the
// manifest too means a stray manifest moved into another directory
// still names itself.
type Manifest struct {
	SnapshotID        string    `toml:"snapshot_id"`
	CreatedAt         time.Time `toml:"created_at"`
	LastEventID       string    `toml:"last_event_id"`
	SourceWorkspaceID string    `toml:"source_workspace_id"`
	// CapturedComponents lists the top-level entries that were
	// copied into the snapshot. Lets future tooling tell what a
	// snapshot from an older or newer Rex captured without
	// guessing.
	CapturedComponents []string `toml:"captured_components"`
}

// readManifest parses the manifest.toml at path. Returns
// fs.ErrNotExist when missing so callers can distinguish "not a
// snapshot dir" from a real parse failure.
func readManifest(path string) (Manifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := toml.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("snapshot: parse %s: %w", path, err)
	}
	if m.SnapshotID == "" {
		return Manifest{}, errors.New("snapshot: manifest missing snapshot_id")
	}
	return m, nil
}

// writeManifest serializes m to path atomically (tempfile + rename
// in the same dir). Existing manifests are overwritten.
func writeManifest(path string, m Manifest) error {
	body, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("snapshot: marshal manifest: %w", err)
	}
	tmp, err := os.CreateTemp(parentDir(path), ".manifest-*.toml")
	if err != nil {
		return fmt.Errorf("snapshot: tempfile: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("snapshot: write manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("snapshot: close manifest: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("snapshot: rename manifest: %w", err)
	}
	return nil
}

// parentDir returns the directory portion of path. Inlined so the
// few callers that need it don't have to import path/filepath.
func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
