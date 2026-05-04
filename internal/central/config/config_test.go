package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultProducesUsableConfig(t *testing.T) {
	t.Parallel()

	c := Default()
	if c.Server.Addr == "" {
		t.Fatal("default Server.Addr empty")
	}
	if c.Server.ShutdownTimeout == 0 {
		t.Fatal("default Server.ShutdownTimeout zero")
	}
	if c.DB.DSN != "" {
		t.Fatalf("default DB.DSN should be empty, got %q", c.DB.DSN)
	}
}

func TestLoadMissingPathReturnsDefaultsPlusEnv(t *testing.T) {
	// no t.Parallel — t.Setenv is incompatible with parallel.
	t.Setenv("REX_CENTRAL_ADDR", "0.0.0.0:9090")
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if c.Server.Addr != "0.0.0.0:9090" {
		t.Fatalf("env override not applied: %q", c.Server.Addr)
	}
	if c.Server.ShutdownTimeout == 0 {
		t.Fatal("default not preserved when only env was set")
	}
}

func TestLoadParsesTOML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "central.toml")
	body := `
[server]
addr = "0.0.0.0:7000"
shutdown_timeout = "30s"

[db]
dsn = "postgres://rex:secret@db:5432/rex"

[auth]
keys_file = "/etc/rex/keys.toml"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.Addr != "0.0.0.0:7000" {
		t.Errorf("Addr: %q", c.Server.Addr)
	}
	if c.Server.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout: %s", c.Server.ShutdownTimeout)
	}
	if c.DB.DSN != "postgres://rex:secret@db:5432/rex" {
		t.Errorf("DSN: %q", c.DB.DSN)
	}
	if c.Auth.KeysFile != "/etc/rex/keys.toml" {
		t.Errorf("KeysFile: %q", c.Auth.KeysFile)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	// no t.Parallel — t.Setenv is incompatible with parallel.
	dir := t.TempDir()
	path := filepath.Join(dir, "central.toml")
	body := `
[db]
dsn = "postgres://from-file/db"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("REX_CENTRAL_DB_DSN", "postgres://from-env/db")

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DB.DSN != "postgres://from-env/db" {
		t.Errorf("expected env to win over file, got %q", c.DB.DSN)
	}
}

func TestResolveResolvesRelativeKeysFile(t *testing.T) {
	t.Parallel()

	c := Config{Auth: Auth{KeysFile: "keys.toml"}}
	c.Resolve("/etc/rex/central.toml")
	if c.Auth.KeysFile != "/etc/rex/keys.toml" {
		t.Errorf("relative path not resolved against config dir: %q", c.Auth.KeysFile)
	}
}

func TestResolveLeavesAbsolutePathsAlone(t *testing.T) {
	t.Parallel()

	c := Config{Auth: Auth{KeysFile: "/abs/keys.toml"}}
	c.Resolve("/etc/rex/central.toml")
	if c.Auth.KeysFile != "/abs/keys.toml" {
		t.Errorf("absolute path was rewritten: %q", c.Auth.KeysFile)
	}
}
