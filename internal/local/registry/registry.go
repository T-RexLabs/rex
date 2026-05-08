// Package registry is the file-backed list of workspaces the local
// machine knows about (storage.GLOBAL.4 / workspace.LIFE.4). One
// registry.toml at ~/.config/rex/registry.toml; entries land via
// `rex workspace init` and `rex workspace clone`.
//
// workspace.LIFE.5 allows two entries to share metadata.id when
// they originate from different remotes — the (id, remote) pair is
// the disambiguating key, not id alone — so the on-disk shape is a
// list of entries rather than a map.
package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// FileName is the registry's basename under the user config dir.
const FileName = "registry.toml"

// Entry is one row of the registry. Fields cover storage.GLOBAL.4.1
// ("Registry entries hold workspace ID, on-disk path, originating
// remote (if any), and last-seen-active timestamp").
type Entry struct {
	ID       string    `toml:"id"`
	Path     string    `toml:"path"`
	Remote   string    `toml:"remote,omitempty"`
	LastSeen time.Time `toml:"last_seen,omitempty"`
}

// fileShape is the on-disk wrapper. Kept as a top-level [[workspaces]]
// list so the (id, remote) disambiguation rule from workspace.LIFE.5
// stays representable.
type fileShape struct {
	Workspaces []Entry `toml:"workspaces"`
}

// Registry is the in-memory representation.
type Registry struct {
	Entries []Entry
}

// DefaultPath returns the platform user-config-dir's rex/registry.toml.
func DefaultPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("registry: locate user config dir: %w", err)
	}
	return filepath.Join(cfg, "rex", FileName), nil
}

// Load reads and parses path. Returns an empty registry + nil when
// the file does not exist (the natural pre-first-init state).
func Load(path string) (*Registry, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Registry{}, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	var raw fileShape
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	return &Registry{Entries: raw.Workspaces}, nil
}

// Save writes the registry to path atomically (tempfile + rename).
// Creates the parent directory if missing.
func Save(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("registry: mkdir %s: %w", filepath.Dir(path), err)
	}

	// Sort the entries deterministically so two saves of the same
	// state produce byte-identical output.
	sorted := append([]Entry(nil), r.Entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ID != sorted[j].ID {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Remote < sorted[j].Remote
	})

	body, err := toml.Marshal(fileShape{Workspaces: sorted})
	if err != nil {
		return fmt.Errorf("registry: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".registry-*.toml")
	if err != nil {
		return fmt.Errorf("registry: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("registry: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("registry: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("registry: rename: %w", err)
	}
	return nil
}

// Upsert replaces the entry matching (id, remote) or appends a new
// one when no match exists. The (id, remote) tuple is the
// disambiguating key per workspace.LIFE.5. LastSeen is stamped to
// time.Now UTC when zero.
func (r *Registry) Upsert(e Entry) {
	if e.LastSeen.IsZero() {
		e.LastSeen = time.Now().UTC()
	}
	for i, existing := range r.Entries {
		if existing.ID == e.ID && existing.Remote == e.Remote {
			r.Entries[i] = e
			return
		}
	}
	r.Entries = append(r.Entries, e)
}

// Remove deletes the entry matching (id, remote). Returns true when
// something was removed.
func (r *Registry) Remove(id, remote string) bool {
	for i, e := range r.Entries {
		if e.ID == id && e.Remote == remote {
			r.Entries = append(r.Entries[:i], r.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// RemoveByPath drops every entry whose Path matches the given on-disk
// location, regardless of (id, remote). Returns the number of
// entries removed. Useful for cleanup when a workspace is deleted
// or moved.
func (r *Registry) RemoveByPath(path string) int {
	clean := filepath.Clean(path)
	kept := r.Entries[:0]
	removed := 0
	for _, e := range r.Entries {
		if filepath.Clean(e.Path) == clean {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	r.Entries = kept
	return removed
}

// List returns the registered entries sorted by id, remote.
func (r *Registry) List() []Entry {
	out := append([]Entry(nil), r.Entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Remote < out[j].Remote
	})
	return out
}

// Find returns the entry matching (id, remote), or nil if not found.
func (r *Registry) Find(id, remote string) *Entry {
	for i, e := range r.Entries {
		if e.ID == id && e.Remote == remote {
			return &r.Entries[i]
		}
	}
	return nil
}

// Validate checks an entry for the minimal shape needed by Upsert.
// IDs must be non-empty and contain no whitespace; Path must be
// absolute.
func (e Entry) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("registry: entry id is required")
	}
	if strings.ContainsAny(e.ID, " \t\n") {
		return fmt.Errorf("registry: id %q contains whitespace", e.ID)
	}
	if !filepath.IsAbs(e.Path) {
		return fmt.Errorf("registry: path %q must be absolute", e.Path)
	}
	return nil
}
