package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAdHocSpec drops a target spec into the test workspace so
// `rex spec ask` / `amend` have something to load. Helper kept
// local to this test file because the spec body shape is shared
// across the two commands.
func writeAdHocSpec(t *testing.T, root, id, name string) {
	t.Helper()
	dir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "spec_version: 1\nmetadata:\n  id: " + id +
		"\n  name: " + name +
		"\n  state: draft\ntasks:\n  - id: only-task\n    description: TODO\n    state: todo\n"
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
}

// TestSpecAskRequiresPrompt confirms a missing prompt arg is a
// clear error rather than a silently-empty harness session.
func TestSpecAskRequiresPrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	writeAdHocSpec(t, dir, "subject", "Subject")

	out, err := executeCommand(t, "spec", "ask", "subject", "--workspace", dir)
	if err == nil {
		t.Fatalf("expected error: %s", out)
	}
	if !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestSpecAskRejectsUnknownSpec covers the no-such-spec path.
func TestSpecAskRejectsUnknownSpec(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := executeCommand(t, "spec", "ask", "ghost", "explain it",
		"--workspace", dir)
	if err == nil {
		t.Fatal("expected unknown-spec error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestSpecAskRejectsBadID ensures IDs that aren't kebab-case get
// rejected immediately rather than failing later in the loader.
func TestSpecAskRejectsBadID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, err := executeCommand(t, "spec", "ask", "Bad ID", "explain",
		"--workspace", dir)
	if err == nil {
		t.Fatal("expected kebab-case error")
	}
	if !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestSpecAmendRejectsBadHarness covers the registry-membership
// check on --harness.
func TestSpecAmendRejectsBadHarness(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	writeAdHocSpec(t, dir, "subject", "Subject")

	_, err := executeCommand(t, "spec", "amend", "subject", "tighten it",
		"--workspace", dir, "--harness", "imaginary")
	if err == nil {
		t.Fatal("expected unknown-harness error")
	}
	if !strings.Contains(err.Error(), "unknown harness") {
		t.Fatalf("wrong error: %v", err)
	}
}
