package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncUsesUserConfigDefaultRemote proves that a user-config
// `default_remote` value drives `rex push` when --remote isn't
// passed. The sync attempt fails because the URL points nowhere,
// but the error wording confirms which remote name was used.
func TestSyncUsesUserConfigDefaultRemote(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml"),
	); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("default_remote = \"staging\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Empty remotes registry → push will fail with "remote 'staging'
	// not registered" (proves default_remote was consulted).
	remotesPath := filepath.Join(t.TempDir(), "remotes.toml")

	_, err := executeCommand(t, "push",
		"--workspace", dir,
		"--remotes-file", remotesPath,
		"--user-config", cfgPath,
	)
	if err == nil {
		t.Fatal("expected push to fail without a registered remote")
	}
	if !strings.Contains(err.Error(), `"staging"`) {
		t.Fatalf("error should reference the user-config remote 'staging': %v", err)
	}
}

// TestSyncExplicitRemoteOverridesUserConfig proves --remote wins
// over user-config default_remote.
func TestSyncExplicitRemoteOverridesUserConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml"),
	); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("default_remote = \"staging\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	remotesPath := filepath.Join(t.TempDir(), "remotes.toml")

	_, err := executeCommand(t, "push",
		"--workspace", dir,
		"--remote", "explicit-name",
		"--remotes-file", remotesPath,
		"--user-config", cfgPath,
	)
	if err == nil {
		t.Fatal("expected push to fail")
	}
	if !strings.Contains(err.Error(), `"explicit-name"`) {
		t.Fatalf("--remote should override user-config: %v", err)
	}
}
