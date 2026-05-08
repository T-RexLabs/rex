package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuietSuppressesConfirmations covers cli.FMT.3 — the root
// --quiet persistent flag suppresses confirmation prints from
// state-changing commands. Verbose default behaviour is asserted
// elsewhere; here we just check the flag has bite.
func TestQuietSuppressesConfirmations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(t.TempDir(), "reg.toml")

	// workspace init: noisy by default.
	out, err := executeCommand(t, "workspace", "init", dir,
		"--registry-file", registryPath)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "Initialized rex workspace") {
		t.Fatalf("default init should print confirmation: %q", out)
	}

	// spec create + repo link with --quiet should print nothing
	// to stdout. (Subdirectories already exist from init.)
	out, err = executeCommand(t, "spec", "create", "--workspace", dir, "--quiet", "demo")
	if err != nil {
		t.Fatalf("spec create --quiet: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--quiet should suppress confirmation; got %q", out)
	}

	// Same for repo link.
	if err := os.MkdirAll(filepath.Join(dir, "vendored"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out, err = executeCommand(t, "repo", "link", "--workspace", dir, "--quiet", "vendored")
	if err != nil {
		t.Fatalf("repo link --quiet: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--quiet should suppress repo link confirmation; got %q", out)
	}
}
