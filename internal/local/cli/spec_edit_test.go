package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeEditor writes a tiny shell script that appends `payload`
// to its file argument and sets $EDITOR to its path. Returns a
// cleanup that restores the prior $EDITOR. Skips on Windows where
// /bin/sh is unavailable.
func installFakeEditor(t *testing.T, payload string) func() {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake editor uses /bin/sh; skipping on windows")
	}
	dir := t.TempDir()
	editorPath := filepath.Join(dir, "fake-editor.sh")
	script := "#!/bin/sh\nprintf '%s' " + shellSingleQuote(payload) + " >> \"$1\"\n"
	if err := os.WriteFile(editorPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	prev, hadPrev := os.LookupEnv("EDITOR")
	if err := os.Setenv("EDITOR", editorPath); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	return func() {
		if hadPrev {
			_ = os.Setenv("EDITOR", prev)
		} else {
			_ = os.Unsetenv("EDITOR")
		}
	}
}

// shellSingleQuote wraps s in single quotes for safe inclusion in a
// /bin/sh script.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestSpecEditAppliesEditAndValidates(t *testing.T) {
	// Not t.Parallel(): all three tests in this file mutate $EDITOR;
	// running them concurrently would let one test race the other's
	// fake editor script.
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	if _, err := executeCommand(t, "spec", "create", "--workspace", dir, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	// The fake editor appends a comment line — a no-op edit that
	// keeps the spec valid. We assert the file picked up the change
	// and the command exited 0.
	cleanup := installFakeEditor(t, "\n# touched-by-edit-test\n")
	defer cleanup()

	out, err := executeCommand(t, "spec", "edit", "--workspace", dir, "demo")
	if err != nil {
		t.Fatalf("spec edit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("expected post-edit validate to print ok, got: %q", out)
	}

	body, err := os.ReadFile(filepath.Join(dir, ".rex", "specs", "demo.yaml"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(body), "touched-by-edit-test") {
		t.Fatalf("editor change not applied; body:\n%s", body)
	}
}

func TestSpecEditFailsValidationOnBadEdit(t *testing.T) {
	// Not t.Parallel(): all three tests in this file mutate $EDITOR;
	// running them concurrently would let one test race the other's
	// fake editor script.
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	if _, err := executeCommand(t, "spec", "create", "--workspace", dir, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	// Append an unknown top-level key — strict-mode validate flags it.
	cleanup := installFakeEditor(t, "\nunknown_top_level_key: nope\n")
	defer cleanup()

	_, err := executeCommand(t, "spec", "edit", "--workspace", dir, "demo")
	if err == nil {
		t.Fatal("expected validation error after a broken edit")
	}
	if !strings.Contains(err.Error(), "validation errors after edit") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestSpecEditUnknownIDErrors(t *testing.T) {
	// Not t.Parallel(): all three tests in this file mutate $EDITOR;
	// running them concurrently would let one test race the other's
	// fake editor script.
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	_, err := executeCommand(t, "spec", "edit", "--workspace", dir, "ghost")
	if err == nil {
		t.Fatal("expected error for unknown spec")
	}
	if !strings.Contains(err.Error(), "stat ") && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("wrong error: %v", err)
	}
}
