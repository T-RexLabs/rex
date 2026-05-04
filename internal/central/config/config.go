// Package config loads the central node's bring-up configuration
// from /etc/rex/central.toml (or the path set by --config), then
// overlays REX_CENTRAL_* environment variables, and lets the CLI
// flag layer overlay one more time on top.
//
// Precedence (lowest to highest) per central-node.DEPLOY.2:
//
//   1. Built-in defaults (Default()).
//   2. The TOML config file. The file is for "non-secret
//      defaults, holding everything that's safe to commit to a
//      git repo or a docker image".
//   3. Environment variables. The file is for "secrets and
//      per-deployment overrides", per DEPLOY.2.
//   4. CLI flags from cmd/rex-central/serve. Operators
//      diagnosing issues can always override anything from the
//      command line.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Default returns a Config populated with the built-in defaults.
// These are the values a fresh `rex-central serve` uses when no
// config file, env vars, or flags are present — i.e. the
// dev/test path used by the existing in-process tests.
func Default() Config {
	return Config{
		Server: Server{
			Addr:            "127.0.0.1:8080",
			ShutdownTimeout: 15 * time.Second,
		},
		Log: Log{
			Level:  "info",
			Format: "json",
		},
		Backup: Backup{
			Cadence: 24 * time.Hour,
		},
		Bootstrap: Bootstrap{
			TokenFile: "/var/lib/rex/bootstrap.token",
		},
	}
}

// Config is the structured shape of /etc/rex/central.toml.
// Every field maps to a TOML key in the obvious way; embedded
// structs become [section] tables.
type Config struct {
	Server    Server    `toml:"server"`
	DB        DB        `toml:"db"`
	Auth      Auth      `toml:"auth"`
	Log       Log       `toml:"log"`
	Backup    Backup    `toml:"backup"`
	Bootstrap Bootstrap `toml:"bootstrap"`
}

// Server holds HTTP server settings.
type Server struct {
	// Addr is the TCP address rex-central listens on. Defaults
	// to 127.0.0.1:8080. Bind 0.0.0.0:<port> in container
	// deployments where a reverse proxy fronts the binary.
	Addr string `toml:"addr"`
	// ShutdownTimeout is the max duration rex-central waits
	// for in-flight requests to finish before forcing exit on
	// SIGTERM/SIGINT.
	ShutdownTimeout time.Duration `toml:"shutdown_timeout"`
}

// DB holds Postgres connection settings.
type DB struct {
	// DSN is a libpq-style connection string. Empty means "use
	// the in-memory store" — fine for dev, useless in
	// production. The bundled docker-compose deployment sets
	// this to the postgres service inside the compose network.
	DSN string `toml:"dsn"`
}

// Auth holds the authorized-keys file location.
type Auth struct {
	// KeysFile is a path to the TOML file holding ed25519
	// public keys the central trusts (sync.SEC.1). Empty means
	// "skip verification" — the dev/test path; production
	// deployments must always set it.
	KeysFile string `toml:"keys_file"`
}

// Log configures the structured logger (HEALTH.3).
type Log struct {
	// Level is one of debug, info, warn, error. Anything else
	// defaults to info — typo-tolerant.
	Level string `toml:"level"`
	// Format is "json" (default, what HEALTH.3 requires) or
	// "text" for local-dev readability.
	Format string `toml:"format"`
}

// Bootstrap configures where the admin bootstrap token (BOOT.1)
// gets persisted to disk on first start with an empty database.
type Bootstrap struct {
	// TokenFile is the host-filesystem path the token is
	// written to. Empty disables file persistence (the token
	// is still logged at WARN level on each startup until
	// redeemed). The bundled compose mounts the rex-state
	// volume at /var/lib/rex so the default lands there.
	TokenFile string `toml:"token_file"`
}

// Backup configures the scheduled pg_dump (BACKUP.1) and the
// restore validator (BACKUP.3).
type Backup struct {
	// Dir is the host-mounted directory the scheduler writes
	// dumps to. Empty disables the scheduler entirely (one-shot
	// `rex-central backup` still works with --output).
	Dir string `toml:"dir"`

	// Cadence is how often the scheduler fires; default 24h
	// per BACKUP.1. Anything <= 0 disables the scheduler.
	Cadence time.Duration `toml:"cadence"`

	// Retention caps the number of dumps kept on disk; older
	// files are deleted after each successful run. 0 means
	// "keep forever" (an external retention policy applies).
	Retention int `toml:"retention"`
}

// Load reads path as TOML and overlays REX_CENTRAL_* env vars on
// top. Missing path is a soft failure: returns Default() with
// env-vars overlaid, so a fresh deployment that doesn't ship a
// config file still starts cleanly. Any other error (parse
// failure, permissions) is returned to the caller.
//
// Empty path skips the file load entirely — used by the
// dev/test path that has no config file at all.
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		body, err := os.ReadFile(path)
		switch {
		case err == nil:
			if _, derr := toml.Decode(string(body), &c); derr != nil {
				return c, fmt.Errorf("config: decode %s: %w", path, derr)
			}
		case os.IsNotExist(err):
			// Soft failure — let the caller proceed on defaults.
		default:
			return c, fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	overlayEnv(&c)
	return c, nil
}

// DefaultPath is the canonical config-file location the central
// looks for in production deployments. Aligned with DEPLOY.2.
const DefaultPath = "/etc/rex/central.toml"

// PathOrDefault returns explicit when set, else DefaultPath. Used
// by the CLI to thread `--config` through Load().
func PathOrDefault(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return DefaultPath
}

// overlayEnv applies REX_CENTRAL_* env vars on top of c. Each
// var maps 1-1 to a config key. Unset vars leave c unchanged.
func overlayEnv(c *Config) {
	if v := os.Getenv("REX_CENTRAL_ADDR"); v != "" {
		c.Server.Addr = v
	}
	if v := os.Getenv("REX_CENTRAL_SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Server.ShutdownTimeout = d
		}
	}
	if v := os.Getenv("REX_CENTRAL_DB_DSN"); v != "" {
		c.DB.DSN = v
	}
	if v := os.Getenv("REX_CENTRAL_KEYS_FILE"); v != "" {
		c.Auth.KeysFile = v
	}
	if v := os.Getenv("REX_CENTRAL_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("REX_CENTRAL_LOG_FORMAT"); v != "" {
		c.Log.Format = v
	}
	if v := os.Getenv("REX_CENTRAL_BACKUP_DIR"); v != "" {
		c.Backup.Dir = v
	}
	if v := os.Getenv("REX_CENTRAL_BACKUP_CADENCE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Backup.Cadence = d
		}
	}
	if v := os.Getenv("REX_CENTRAL_BOOTSTRAP_TOKEN_FILE"); v != "" {
		c.Bootstrap.TokenFile = v
	}
}

// Resolve normalizes paths inside c so handlers don't have to
// re-resolve them at use sites. Currently only KeysFile is
// path-shaped; this is the seam to add future relative-path
// resolution against the config file's directory.
func (c *Config) Resolve(configFilePath string) {
	if c.Auth.KeysFile != "" && !filepath.IsAbs(c.Auth.KeysFile) {
		if configFilePath != "" {
			c.Auth.KeysFile = filepath.Join(filepath.Dir(configFilePath), c.Auth.KeysFile)
		}
	}
}
