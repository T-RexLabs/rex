package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initWorkspaceWithRegistry boots a workspace at dir with --registry-file
// pointing at registryPath so tests don't write into the user's
// real ~/.config/rex/registry.toml.
func initWorkspaceWithRegistry(t *testing.T, dir, registryPath string) {
	t.Helper()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", registryPath,
	); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
}

func TestWorkspaceInitRegistersInRegistry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(t.TempDir(), "registry.toml")

	initWorkspaceWithRegistry(t, dir, registryPath)

	body, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(body), "[[workspaces]]") {
		t.Fatalf("expected workspaces table:\n%s", body)
	}
	if !strings.Contains(string(body), "path =") {
		t.Fatalf("expected path field:\n%s", body)
	}
}

func TestWorkspaceListReadsRegistry(t *testing.T) {
	t.Parallel()

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	registryPath := filepath.Join(t.TempDir(), "registry.toml")

	if _, err := executeCommand(t, "init", dir1,
		"--id", "alpha", "--registry-file", registryPath,
	); err != nil {
		t.Fatalf("init alpha: %v", err)
	}
	if _, err := executeCommand(t, "init", dir2,
		"--id", "beta", "--registry-file", registryPath,
	); err != nil {
		t.Fatalf("init beta: %v", err)
	}

	out, err := executeCommand(t, "workspace", "list", "--registry-file", registryPath)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("list output: %q", out)
	}
	if !strings.Contains(out, "registry") {
		t.Fatalf("list should label source as 'registry': %q", out)
	}
}

func TestWorkspaceListJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(t.TempDir(), "registry.toml")
	initWorkspaceWithRegistry(t, dir, registryPath)

	out, err := executeCommand(t, "workspace", "list",
		"--registry-file", registryPath, "--json",
	)
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["state"] != "active" {
		t.Fatalf("state: %v", rows[0])
	}
}

func TestWorkspaceListEmptyRegistryFallsBackToCwd(t *testing.T) {
	t.Parallel()

	registryPath := filepath.Join(t.TempDir(), "registry.toml")
	out, err := executeCommand(t, "workspace", "list", "--registry-file", registryPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// CWD may or may not be a workspace depending on where the
	// suite runs (a dev's working tree often has .rex/ from prior
	// init; CI's fresh checkout doesn't). Either path is fine —
	// "(cwd)" labels the fallback row when one resolves; the
	// "no workspaces" hint (case-insensitive) lands otherwise.
	if !strings.Contains(strings.ToLower(out), "no workspaces") && !strings.Contains(out, "(cwd)") {
		t.Fatalf("expected empty hint or cwd fallback, got: %q", out)
	}
}

func TestWorkspaceListMarksMissingPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(t.TempDir(), "registry.toml")
	initWorkspaceWithRegistry(t, dir, registryPath)

	// Delete the workspace tree without unregistering it; list
	// should flag the entry as (missing) but still surface it.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	out, err := executeCommand(t, "workspace", "list", "--registry-file", registryPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "(missing)") {
		t.Fatalf("expected (missing) marker: %q", out)
	}
}
