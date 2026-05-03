package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksListEmptyWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "hooks", "list",
		"--workspace", dir,
		"--no-global",
	)
	if err != nil {
		t.Fatalf("hooks list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no hooks installed") {
		t.Fatalf("empty workspace should report none: %s", out)
	}
}

func TestHooksListReportsExecutableAndNonExecutable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	hooksDir := filepath.Join(dir, ".rex", "hooks")
	if err := os.WriteFile(filepath.Join(hooksDir, "post-run-completed"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write exec hook: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "post-spec-edit"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write non-exec hook: %v", err)
	}
	// Sidecar config must NOT appear as a hook itself.
	if err := os.WriteFile(filepath.Join(hooksDir, "post-run-completed.config.toml"), []byte("timeout = \"60s\"\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	out, err := executeCommand(t, "hooks", "list",
		"--workspace", dir,
		"--no-global",
	)
	if err != nil {
		t.Fatalf("hooks list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "post-run-completed") {
		t.Fatalf("missing exec hook: %s", out)
	}
	if !strings.Contains(out, "post-spec-edit") {
		t.Fatalf("missing non-exec hook: %s", out)
	}
	if !strings.Contains(out, "no (skipped)") {
		t.Fatalf("non-exec status not surfaced: %s", out)
	}
	if strings.Contains(out, ".config.toml") {
		t.Fatalf("sidecar should not appear: %s", out)
	}
}

func TestHooksListGlobalScope(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", wsDir); err != nil {
		t.Fatalf("init: %v", err)
	}
	globalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalDir, "post-any"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write global hook: %v", err)
	}

	out, err := executeCommand(t, "hooks", "list",
		"--workspace", wsDir,
		"--global-dir", globalDir,
	)
	if err != nil {
		t.Fatalf("hooks list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "post-any") {
		t.Fatalf("missing global hook: %s", out)
	}
	if !strings.Contains(out, "global") {
		t.Fatalf("scope label missing: %s", out)
	}
}

func TestHooksListJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	hooksDir := filepath.Join(dir, ".rex", "hooks")
	if err := os.WriteFile(filepath.Join(hooksDir, "post-run-completed"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	out, err := executeCommand(t, "hooks", "list",
		"--workspace", dir,
		"--no-global",
		"--json",
	)
	if err != nil {
		t.Fatalf("hooks list --json: %v\n%s", err, out)
	}
	for _, line := range splitNonEmpty(out) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("non-JSON line: %q", line)
		}
		if entry["name"] == nil {
			t.Fatalf("entry missing name: %v", entry)
		}
	}
}
