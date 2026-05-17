package remotes

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

// FileName is the registry's basename under the user config dir.
const FileName = "remotes.toml"

// Remote is one named remote. Stored under [<Name>] in the TOML file
// with field names matching the toml tags below.
type Remote struct {
	Name        string    `toml:"-"`
	URL         string    `toml:"url"`
	Fingerprint string    `toml:"fingerprint,omitempty"`
	AddedAt     time.Time `toml:"added_at"`
	LastSeen    time.Time `toml:"last_seen,omitempty"`
}

// Registry is the in-memory representation of remotes.toml.
type Registry struct {
	Remotes map[string]Remote
}

// nameRE constrains remote names to a kebab-ish identifier so they
// can land in a TOML section header without quoting and round-trip
// cleanly. Matches the same shape we use for workspace ids and
// metadata.id's.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsValidName reports whether s is a usable remote name.
func IsValidName(s string) bool { return nameRE.MatchString(s) }

// EnvPath is the env var name honoured by DefaultPath as an
// explicit override. Set by tests + scripts that don't want to
// touch the platform user-config dir.
const EnvPath = "REX_REMOTES_FILE"

// DefaultPath resolves the platform user-config-dir's
// rex/remotes.toml. The REX_REMOTES_FILE env var takes
// precedence when set — same shape as the identity store's
// REX_IDENTITY_DIR. Returns an error only when both the env
// override is empty and the platform lookup fails.
func DefaultPath() (string, error) {
	if v := os.Getenv(EnvPath); v != "" {
		return v, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("remotes: locate user config dir: %w", err)
	}
	return filepath.Join(cfg, "rex", FileName), nil
}

// Load reads and parses path. Returns an empty registry + nil when
// the file does not exist (the natural pre-first-add state).
func Load(path string) (*Registry, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Registry{Remotes: map[string]Remote{}}, nil
		}
		return nil, fmt.Errorf("remotes: read %s: %w", path, err)
	}
	reg, err := ParseBytes(body)
	if err != nil {
		return nil, fmt.Errorf("remotes: parse %s: %w", path, err)
	}
	return reg, nil
}

// ParseBytes is the filesystem-agnostic parse path: callers with
// raw bytes (e.g. the central web shell projecting from the
// GitStore) feed them straight in without writing to a temp file.
// Returns an empty registry + nil when body is empty.
func ParseBytes(body []byte) (*Registry, error) {
	if len(body) == 0 {
		return &Registry{Remotes: map[string]Remote{}}, nil
	}
	var raw map[string]Remote
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := &Registry{Remotes: make(map[string]Remote, len(raw))}
	for name, r := range raw {
		r.Name = name
		out.Remotes[name] = r
	}
	return out, nil
}

// Save writes the registry to path atomically (tempfile + rename).
// Creates the parent directory if missing. Permissions: 0644 on the
// file (no secrets); the parent dir uses the platform umask.
func Save(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("remotes: mkdir %s: %w", filepath.Dir(path), err)
	}

	// Encode to a deterministic, name-sorted output so two runs
	// against the same registry produce byte-identical files.
	keys := make([]string, 0, len(r.Remotes))
	for k := range r.Remotes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	encoded := strings.Builder{}
	for _, name := range keys {
		entry := r.Remotes[name]
		fmt.Fprintf(&encoded, "[%s]\n", name)
		body, err := toml.Marshal(entry)
		if err != nil {
			return fmt.Errorf("remotes: marshal %q: %w", name, err)
		}
		encoded.WriteString(string(body))
		encoded.WriteString("\n")
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".remotes-*.toml")
	if err != nil {
		return fmt.Errorf("remotes: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(encoded.String()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("remotes: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("remotes: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("remotes: rename: %w", err)
	}
	return nil
}

// Add registers a new remote. Returns an error when name collides
// with an existing entry (use Set to overwrite). The added_at field
// is stamped to time.Now in UTC if zero.
func (r *Registry) Add(remote Remote) error {
	if remote.Name == "" {
		return errors.New("remotes: Add requires a name")
	}
	if !IsValidName(remote.Name) {
		return fmt.Errorf("remotes: name %q must be kebab-case", remote.Name)
	}
	if remote.URL == "" {
		return errors.New("remotes: Add requires a url")
	}
	if _, exists := r.Remotes[remote.Name]; exists {
		return fmt.Errorf("remotes: %q already registered", remote.Name)
	}
	if remote.AddedAt.IsZero() {
		remote.AddedAt = time.Now().UTC()
	}
	if r.Remotes == nil {
		r.Remotes = make(map[string]Remote)
	}
	r.Remotes[remote.Name] = remote
	return nil
}

// Set is the upsert variant. Stamps AddedAt only when the remote is
// new and the field is zero.
func (r *Registry) Set(remote Remote) error {
	if remote.Name == "" {
		return errors.New("remotes: Set requires a name")
	}
	if !IsValidName(remote.Name) {
		return fmt.Errorf("remotes: name %q must be kebab-case", remote.Name)
	}
	if remote.URL == "" {
		return errors.New("remotes: Set requires a url")
	}
	if r.Remotes == nil {
		r.Remotes = make(map[string]Remote)
	}
	if _, exists := r.Remotes[remote.Name]; !exists && remote.AddedAt.IsZero() {
		remote.AddedAt = time.Now().UTC()
	}
	r.Remotes[remote.Name] = remote
	return nil
}

// Remove deletes the named remote. Returns an error if not found.
func (r *Registry) Remove(name string) error {
	if r.Remotes == nil {
		return fmt.Errorf("remotes: %q not found", name)
	}
	if _, ok := r.Remotes[name]; !ok {
		return fmt.Errorf("remotes: %q not found", name)
	}
	delete(r.Remotes, name)
	return nil
}

// Get returns the named remote.
func (r *Registry) Get(name string) (Remote, bool) {
	if r.Remotes == nil {
		return Remote{}, false
	}
	got, ok := r.Remotes[name]
	return got, ok
}

// List returns every remote in lex order by name.
func (r *Registry) List() []Remote {
	if r.Remotes == nil {
		return nil
	}
	out := make([]Remote, 0, len(r.Remotes))
	for _, v := range r.Remotes {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
