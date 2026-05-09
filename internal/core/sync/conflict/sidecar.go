// Package conflict owns the on-disk sidecar format that records an
// unresolved 3-way merge for a git-merged entity (sync.GIT.3).
//
// When the rebase pipeline produces a conflict, two artifacts land
// alongside the affected entity:
//
//  1. The entity file itself is overwritten with conflict-marker text
//     (the MarkerLocal/Separator/Remote sequence from package merge3),
//     so the user can edit it directly.
//
//  2. A `<file>.conflict` sidecar TOML carries the structured
//     description: where the entity came from, what revisions were
//     involved, and which line ranges conflict. It exists for two
//     reasons:
//
//     - `rex spec validate` and `rex run start` refuse to operate on
//     any entity whose sidecar is present (sync.GIT.3 last clause),
//     so the sidecar is the authoritative "this is conflicted" mark
//     that survives even if the user accidentally removes the
//     in-file markers.
//     - `rex sync resolve` reads the sidecar to confirm it is
//     resolving a real conflict and to emit the resulting event with
//     full provenance.
package conflict

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// SidecarSuffix is appended to the entity path to derive the sidecar
// path. Same convention as Git's `.orig` for backup files.
const SidecarSuffix = ".conflict"

// Sidecar is the on-disk shape of a `.conflict` file.
type Sidecar struct {
	// Entity is the `.rex/`-relative path of the conflicted file.
	Entity string `toml:"entity"`
	// Remote is the named remote against which the rebase was
	// attempted.
	Remote string `toml:"remote"`
	// BaseRevision is the merge-base revision id (the last
	// known-agreed revision between local and remote at rebase
	// time). May be empty when the entity has no known base.
	BaseRevision string `toml:"base_revision,omitempty"`
	// LocalRevision identifies the local content at rebase time.
	// Format: hex SHA-256 of local content (matches
	// proto.GitContentRevision).
	LocalRevision string `toml:"local_revision"`
	// RemoteRevision is the server's current revision for this
	// entity (proto.GitEntity.Revision returned on /sync/git GET
	// or in the 409 conflict body).
	RemoteRevision string `toml:"remote_revision"`
	// CreatedAt is when the sidecar was written.
	CreatedAt time.Time `toml:"created_at"`
	// Hunks records the unresolved regions reported by merge3.
	// The same line numbers appear in the conflict markers in the
	// entity file itself.
	Hunks []Hunk `toml:"hunks"`
}

// Hunk mirrors merge3.ConflictHunk in TOML-friendly form.
type Hunk struct {
	BaseStart   int      `toml:"base_start"`
	BaseEnd     int      `toml:"base_end"`
	LocalStart  int      `toml:"local_start"`
	LocalEnd    int      `toml:"local_end"`
	RemoteStart int      `toml:"remote_start"`
	RemoteEnd   int      `toml:"remote_end"`
	BaseLines   []string `toml:"base_lines,omitempty"`
	LocalLines  []string `toml:"local_lines,omitempty"`
	RemoteLines []string `toml:"remote_lines,omitempty"`
}

// SidecarPathFor returns the conflict-sidecar path for an entity
// path. Both inputs and outputs are absolute or both are relative;
// the suffix is simply appended.
func SidecarPathFor(entityPath string) string {
	return entityPath + SidecarSuffix
}

// Write atomically persists s to dest. Refuses to overwrite via
// rename failure if dest is on a different filesystem from the
// surrounding temp file (same atomicity rule the rest of `.rex/` uses).
func Write(dest string, s Sidecar) error {
	if s.Entity == "" {
		return errors.New("conflict: sidecar requires Entity")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("conflict: mkdir %s: %w", filepath.Dir(dest), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".conflict-*")
	if err != nil {
		return fmt.Errorf("conflict: tempfile: %w", err)
	}
	if err := toml.NewEncoder(tmp).Encode(s); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("conflict: encode %s: %w", dest, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("conflict: close %s: %w", dest, err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("conflict: rename %s: %w", dest, err)
	}
	return nil
}

// Read loads a sidecar from disk. Returns fs.ErrNotExist when the
// file is missing so callers can branch with errors.Is.
func Read(path string) (Sidecar, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Sidecar{}, err
	}
	var s Sidecar
	if err := toml.Unmarshal(body, &s); err != nil {
		return Sidecar{}, fmt.Errorf("conflict: decode %s: %w", path, err)
	}
	return s, nil
}

// Clear removes the sidecar at path. Missing-file is a no-op so
// `rex sync resolve` can be re-run safely without a fresh stat call.
func Clear(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("conflict: remove %s: %w", path, err)
	}
	return nil
}

// Exists reports whether a sidecar exists at the canonical location
// for entityPath.
func Exists(entityPath string) (bool, error) {
	_, err := os.Stat(SidecarPathFor(entityPath))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// IsConflicted reports whether entityPath is currently flagged as
// conflicted — either via a sidecar OR via in-file conflict markers
// the user has not yet resolved. The two checks are independent so
// `rex spec validate` refuses both "sidecar exists but markers were
// edited out" and "sidecar was deleted but markers remain".
func IsConflicted(entityPath string) (bool, error) {
	if has, err := Exists(entityPath); err != nil {
		return false, err
	} else if has {
		return true, nil
	}
	body, err := os.ReadFile(entityPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return HasMarkers(body), nil
}

// HasMarkers reports whether body contains any of the merge3 conflict
// markers. A pure check on bytes — no allocation beyond a slice scan.
func HasMarkers(body []byte) bool {
	for _, m := range markerSet {
		if containsLine(body, m) {
			return true
		}
	}
	return false
}

var markerSet = []string{
	"<<<<<<< local",
	"=======",
	">>>>>>> remote",
}

// containsLine reports whether body contains marker at the start of
// any line. Matches the exact form merge3 emits (no leading spaces,
// followed by either end-of-input or a newline).
func containsLine(body []byte, marker string) bool {
	src := string(body)
	for {
		idx := indexLineStart(src, marker)
		if idx < 0 {
			return false
		}
		end := idx + len(marker)
		if end == len(src) || src[end] == '\n' {
			return true
		}
		src = src[end:]
	}
}

// indexLineStart returns the first index of marker in src that is at
// the start of a line (offset 0 or preceded by '\n'), or -1.
func indexLineStart(src, marker string) int {
	off := 0
	for {
		idx := indexFrom(src, marker, off)
		if idx < 0 {
			return -1
		}
		if idx == 0 || src[idx-1] == '\n' {
			return idx
		}
		off = idx + 1
	}
}

// indexFrom is strings.Index with a starting offset. Inlined to avoid
// a strings import for one call site.
func indexFrom(src, marker string, off int) int {
	if off >= len(src) {
		return -1
	}
	rel := -1
	for i := off; i+len(marker) <= len(src); i++ {
		if src[i:i+len(marker)] == marker {
			rel = i
			break
		}
	}
	return rel
}
