// Package userconfig is the file-backed per-user config that
// powers ~/.config/rex/config.toml (storage.GLOBAL.2). Holds
// defaults the CLI consults when a flag is omitted: active
// identity, default remote, log level, and hook search paths.
//
// One file, one Load: callers read it once at startup and feed
// individual fields into their own subsystems. The package itself
// has no opinion on what the values mean — see each consumer for
// resolution rules.
package userconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName is the canonical basename under the user config dir.
const FileName = "config.toml"

// Config is the on-disk shape. Every field is optional; missing
// fields fall back to subsystem-side defaults.
type Config struct {
	// ActiveIdentity selects which keypair under
	// ~/.config/rex/identity/ is the default signer
	// (storage.GLOBAL.3 / identity-and-trust.KEY.3).
	ActiveIdentity string `toml:"active_identity,omitempty"`
	// DefaultRemote is the remote name `rex push/pull/sync`
	// target when --remote is omitted.
	DefaultRemote string `toml:"default_remote,omitempty"`
	// LogLevel is one of "debug", "info", "warn", "error";
	// consumers map to their own logger primitives.
	LogLevel string `toml:"log_level,omitempty"`
	// HookSearchPaths extends the default hook search to
	// additional directories (storage.GLOBAL.6 covers the
	// canonical ~/.config/rex/hooks/; this lets ops add others).
	HookSearchPaths []string `toml:"hook_search_paths,omitempty"`
}

// DefaultPath resolves the platform user-config-dir's
// rex/config.toml.
func DefaultPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("userconfig: locate user config dir: %w", err)
	}
	return filepath.Join(cfg, "rex", FileName), nil
}

// Load reads and parses path. Returns a zero Config + nil error
// when the file does not exist (the natural pre-first-config state).
func Load(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("userconfig: read %s: %w", path, err)
	}
	var c Config
	if err := toml.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("userconfig: parse %s: %w", path, err)
	}
	return &c, nil
}

// Save writes c to path atomically (tempfile + rename). Creates
// the parent directory if missing. Empty fields are omitted from
// the encoded output thanks to the omitempty tags.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("userconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	body, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("userconfig: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("userconfig: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("userconfig: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("userconfig: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("userconfig: rename: %w", err)
	}
	return nil
}

// LoadDefault is the convenience entry point: resolves the
// platform path, loads it, and returns the parsed Config (or an
// empty one when the file is missing). Most callers want this.
func LoadDefault() (*Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return Load(path)
}
