package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// completionWorkspace builds a tempdir workspace with two tiny
// specs covering the cases each completer should emit:
//
//   - alpha (one component COMP with two requirements)
//   - beta (two tasks)
//
// Returns the root.
func completionWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	specs := filepath.Join(dir, ".rex", "specs")
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(specs, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	must("alpha.yaml", `spec_version: 1
metadata:
  id: alpha
  name: Alpha
  state: draft
components:
  COMP:
    name: Comp
    requirements:
      "1": one
      "2": two
`)
	must("beta.yaml", `spec_version: 1
metadata:
  id: beta
  name: Beta
  state: draft
tasks:
  - id: task-x
    description: x
    state: todo
  - id: task-y
    description: y
    state: todo
`)
	return dir
}

// runCompletionCommand drives cobra's hidden __complete entry
// point and returns the suggestions it printed plus the bottom
// `:N` directive line. We use the public binary surface rather
// than reaching into the completer functions directly so the
// test exercises the same flag-binding wiring real users hit.
func runCompletionCommand(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := executeCommand(t, append([]string{"__complete"}, args...)...)
	if err != nil {
		t.Fatalf("__complete %v: %v\n%s", args, err, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	results := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Cobra prints `:N` directives on the last lines and
		// "Completion ended with directive: ..." after that.
		// We want only the suggestions before either.
		if strings.HasPrefix(line, ":") {
			break
		}
		if strings.HasPrefix(line, "Completion ended") {
			break
		}
		results = append(results, line)
	}
	return results
}

// TestCompleteSpecIDsListsAllSpecs covers the first-arg completer
// on `rex spec runs <id>`.
func TestCompleteSpecIDsListsAllSpecs(t *testing.T) {
	t.Parallel()

	dir := completionWorkspace(t)
	got := runCompletionCommand(t, "spec", "runs", "--workspace", dir, "")
	want := []string{"alpha", "beta"}
	if !sliceMatches(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestCompleteFromTaskRefsListsAllSpecTaskPairs covers the
// `--from-task` completer on `rex run start`.
func TestCompleteFromTaskRefsListsAllSpecTaskPairs(t *testing.T) {
	t.Parallel()

	dir := completionWorkspace(t)
	got := runCompletionCommand(t,
		"run", "start", "--workspace", dir, "--from-task", "")
	want := []string{"beta.task-x", "beta.task-y"}
	if !sliceMatches(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestCompleteFromTaskRefsFiltersByPrefix mirrors the user's
// experience when they've typed `beta.` and pressed Tab.
func TestCompleteFromTaskRefsFiltersByPrefix(t *testing.T) {
	t.Parallel()

	dir := completionWorkspace(t)
	got := runCompletionCommand(t,
		"run", "start", "--workspace", dir, "--from-task", "beta.task-x")
	want := []string{"beta.task-x"}
	if !sliceMatches(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestCompleteSpecRefsListsACIDs covers `--spec-ref` completion.
// The alpha spec has two requirements under the COMP component.
func TestCompleteSpecRefsListsACIDs(t *testing.T) {
	t.Parallel()

	dir := completionWorkspace(t)
	got := runCompletionCommand(t,
		"run", "start", "--workspace", dir, "--spec-ref", "alpha.")
	want := []string{"alpha.COMP.1", "alpha.COMP.2"}
	if !sliceMatches(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestCompleteTaskFlagDependsOnSpecArg confirms `rex spec runs
// beta --task <Tab>` narrows to that spec's tasks. The
// completer reads args[0] off the already-typed positional.
func TestCompleteTaskFlagDependsOnSpecArg(t *testing.T) {
	t.Parallel()

	dir := completionWorkspace(t)
	got := runCompletionCommand(t,
		"spec", "runs", "--workspace", dir, "beta", "--task", "")
	want := []string{"task-x", "task-y"}
	if !sliceMatches(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// sliceMatches compares two string slices order-insensitive.
func sliceMatches(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			return false
		}
	}
	return true
}
