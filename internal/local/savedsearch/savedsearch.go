// Package savedsearch is the file-backed registry of named search
// queries (search.SAVED.*). Two locations are recognized:
//
//   - per-workspace: <workspaceRoot>/.rex/saved-searches.toml
//     (git-merged with the workspace, travels via sync)
//   - per-user:      ~/.config/rex/saved-searches.toml
//     (local-only, follows the user across workspaces)
//
// The CLI's `rex search saved` subcommands read both, with
// per-workspace shadowing per-user on name collision so a workspace
// can override a global default. Writes default to per-workspace
// per the same instinct (favor the more-specific scope).
package savedsearch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// FileName is the registry's canonical basename (used in both
// per-user and per-workspace locations).
const FileName = "saved-searches.toml"

// SavedSearch is one named query. Stored under [searches.<Name>] in
// the TOML file with field tags below.
type SavedSearch struct {
	Name      string    `toml:"-"`
	Query     string    `toml:"query"`
	CreatedAt time.Time `toml:"created_at,omitempty"`
}

// Registry is the in-memory representation of saved-searches.toml.
type Registry struct {
	Searches map[string]SavedSearch
}

// nameRE constrains saved-search names to a kebab-ish identifier so
// they round-trip in TOML headers without quoting. Same shape used
// by remotes and workspace ids.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsValidName reports whether s is a usable saved-search name.
func IsValidName(s string) bool { return nameRE.MatchString(s) }

// WorkspacePath returns the canonical per-workspace location for
// saved searches. Empty workspaceRoot is treated as ".".
func WorkspacePath(workspaceRoot string) string {
	if workspaceRoot == "" {
		workspaceRoot = "."
	}
	return filepath.Join(workspaceRoot, ".rex", FileName)
}

// UserPath resolves the per-user location under the platform's
// user-config-dir. Returns an error only when the platform lookup
// fails.
func UserPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("savedsearch: locate user config dir: %w", err)
	}
	return filepath.Join(cfg, "rex", FileName), nil
}

// Load reads and parses path. Returns an empty registry + nil when
// the file does not exist (the natural pre-first-save state).
func Load(path string) (*Registry, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Registry{Searches: map[string]SavedSearch{}}, nil
		}
		return nil, fmt.Errorf("savedsearch: read %s: %w", path, err)
	}
	var raw struct {
		Searches map[string]SavedSearch `toml:"searches"`
	}
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("savedsearch: parse %s: %w", path, err)
	}
	out := &Registry{Searches: make(map[string]SavedSearch, len(raw.Searches))}
	for name, s := range raw.Searches {
		s.Name = name
		out.Searches[name] = s
	}
	return out, nil
}

// Save writes the registry to path atomically (tempfile + rename).
// Creates the parent directory if missing. Empty registries write
// an empty file rather than deleting — a pre-existing file at the
// path is replaced to remain idempotent.
func Save(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("savedsearch: mkdir %s: %w", filepath.Dir(path), err)
	}

	keys := make([]string, 0, len(r.Searches))
	for k := range r.Searches {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	encoded := strings.Builder{}
	for _, name := range keys {
		entry := r.Searches[name]
		fmt.Fprintf(&encoded, "[searches.%s]\n", name)
		body, err := toml.Marshal(entry)
		if err != nil {
			return fmt.Errorf("savedsearch: marshal %q: %w", name, err)
		}
		encoded.WriteString(string(body))
		encoded.WriteString("\n")
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".saved-searches-*.toml")
	if err != nil {
		return fmt.Errorf("savedsearch: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(encoded.String()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("savedsearch: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("savedsearch: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("savedsearch: rename: %w", err)
	}
	return nil
}

// Add registers a new saved search. Errors when name collides with
// an existing entry (use Set to overwrite). CreatedAt is stamped to
// time.Now UTC when zero.
func (r *Registry) Add(s SavedSearch) error {
	if s.Name == "" {
		return errors.New("savedsearch: Add requires a name")
	}
	if !IsValidName(s.Name) {
		return fmt.Errorf("savedsearch: name %q must be kebab-case", s.Name)
	}
	if strings.TrimSpace(s.Query) == "" {
		return errors.New("savedsearch: Add requires a query")
	}
	if _, exists := r.Searches[s.Name]; exists {
		return fmt.Errorf("savedsearch: %q already exists", s.Name)
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if r.Searches == nil {
		r.Searches = make(map[string]SavedSearch)
	}
	r.Searches[s.Name] = s
	return nil
}

// Set is the upsert variant. Preserves CreatedAt for existing
// entries; stamps it for new ones.
func (r *Registry) Set(s SavedSearch) error {
	if s.Name == "" {
		return errors.New("savedsearch: Set requires a name")
	}
	if !IsValidName(s.Name) {
		return fmt.Errorf("savedsearch: name %q must be kebab-case", s.Name)
	}
	if strings.TrimSpace(s.Query) == "" {
		return errors.New("savedsearch: Set requires a query")
	}
	if r.Searches == nil {
		r.Searches = make(map[string]SavedSearch)
	}
	if existing, ok := r.Searches[s.Name]; ok {
		if s.CreatedAt.IsZero() {
			s.CreatedAt = existing.CreatedAt
		}
	} else if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	r.Searches[s.Name] = s
	return nil
}

// Remove deletes the named saved search. Errors when not found.
func (r *Registry) Remove(name string) error {
	if r.Searches == nil {
		return fmt.Errorf("savedsearch: %q not found", name)
	}
	if _, ok := r.Searches[name]; !ok {
		return fmt.Errorf("savedsearch: %q not found", name)
	}
	delete(r.Searches, name)
	return nil
}

// Get returns the named saved search.
func (r *Registry) Get(name string) (SavedSearch, bool) {
	if r.Searches == nil {
		return SavedSearch{}, false
	}
	got, ok := r.Searches[name]
	return got, ok
}

// List returns every entry in lex order by name.
func (r *Registry) List() []SavedSearch {
	if r.Searches == nil {
		return nil
	}
	out := make([]SavedSearch, 0, len(r.Searches))
	for _, v := range r.Searches {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MergedView returns the combined list of saved searches, with
// per-workspace entries shadowing per-user ones on name collision.
// The Source field on each entry records where it was loaded from
// so the CLI surface can hint at scope.
func MergedView(workspace, user *Registry) []SavedSearchView {
	merged := map[string]SavedSearchView{}
	if user != nil {
		for _, s := range user.List() {
			v := SavedSearchView{SavedSearch: s, Source: SourceUser}
			merged[s.Name] = v
		}
	}
	if workspace != nil {
		for _, s := range workspace.List() {
			v := SavedSearchView{SavedSearch: s, Source: SourceWorkspace}
			merged[s.Name] = v
		}
	}
	out := make([]SavedSearchView, 0, len(merged))
	for _, v := range merged {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Source enumerates which on-disk location a SavedSearchView came
// from after MergedView resolves precedence.
type Source string

const (
	SourceWorkspace Source = "workspace"
	SourceUser      Source = "user"
)

// SavedSearchView annotates a SavedSearch with its origin source so
// `rex search saved list` can hint at scope without a second
// lookup.
type SavedSearchView struct {
	SavedSearch
	Source Source
}
