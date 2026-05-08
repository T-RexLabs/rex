package userconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	want := &Config{
		ActiveIdentity:  "alice",
		DefaultRemote:   "primary",
		LogLevel:        "info",
		HookSearchPaths: []string{"/etc/rex/hooks", "/opt/rex/hooks"},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), `default_remote = "primary"`) {
		t.Fatalf("missing default_remote:\n%s", body)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ActiveIdentity != want.ActiveIdentity ||
		got.DefaultRemote != want.DefaultRemote ||
		got.LogLevel != want.LogLevel {
		t.Fatalf("round-trip drift: %+v", got)
	}
	if len(got.HookSearchPaths) != 2 || got.HookSearchPaths[1] != "/opt/rex/hooks" {
		t.Fatalf("hook paths: %v", got.HookSearchPaths)
	}
}

func TestEmptyFieldsOmitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := Save(path, &Config{ActiveIdentity: "alice"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ := os.ReadFile(path)
	for _, dropped := range []string{"default_remote", "log_level", "hook_search_paths"} {
		if strings.Contains(string(body), dropped) {
			t.Fatalf("expected %q omitted from output:\n%s", dropped, body)
		}
	}
}

func TestLoadMissingReturnsZero(t *testing.T) {
	t.Parallel()
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if c.ActiveIdentity != "" || c.DefaultRemote != "" {
		t.Fatalf("expected zero Config: %+v", c)
	}
}

func TestLoadParseError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("not = valid = toml"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
