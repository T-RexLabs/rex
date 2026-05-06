package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRecipeSpec drops a minimal spec under .rex/specs/<id>.yaml so
// `--from-task` can resolve it.
func writeRecipeSpec(t *testing.T, workspaceRoot, id, body string) {
	t.Helper()
	dir := filepath.Join(workspaceRoot, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir specs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

func TestRunStartFromTaskShellRecipe(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: greet
    description: say hello
    state: todo
    references: [X.1]
    run:
      kind: shell
      command: ["echo", "hello from {{task.id}}"]
`)

	out, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.greet",
		"--run-id", "from-task-1",
	)
	if err != nil {
		t.Fatalf("run start --from-task: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello from greet") {
		t.Fatalf("rendered prompt token missing from stdout: %s", out)
	}
}

func TestRunStartFromTaskRecordsProvenance(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
      "2": two
tasks:
  - id: greet
    description: say hello
    state: todo
    references: [X.1, X.2]
    run:
      kind: shell
      command: ["echo", "ok"]
`)

	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.greet",
		"--spec-ref", "overview.SEC.2",
		"--run-id", "prov-1",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}

	listOut, err := executeCommand(t, "run", "list", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("run list: %v\n%s", err, listOut)
	}
	dec := json.NewDecoder(strings.NewReader(listOut))
	var found bool
	for dec.More() {
		var s runSummary
		if err := dec.Decode(&s); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if s.RunID != "prov-1" {
			continue
		}
		found = true
		if s.FromTask != "recipes.greet" {
			t.Fatalf("from_task: got %q", s.FromTask)
		}
		want := map[string]bool{
			"overview.SEC.2": false,
			"recipes.X.1":    false,
			"recipes.X.2":    false,
		}
		for _, ref := range s.SpecRefs {
			if _, ok := want[ref]; ok {
				want[ref] = true
			}
		}
		for ref, seen := range want {
			if !seen {
				t.Fatalf("expected spec_ref %q in %v", ref, s.SpecRefs)
			}
		}
	}
	if !found {
		t.Fatalf("did not find run prov-1 in list output: %s", listOut)
	}
}

func TestRunStartFromTaskUnknownSpec(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: greet
    description: greet
    state: todo
    run:
      kind: shell
      command: ["echo", "hi"]
`)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "no-such-spec.task",
	)
	if err == nil {
		t.Fatal("expected error on unknown spec id")
	}
	if !strings.Contains(err.Error(), "no spec with metadata.id") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunStartFromTaskUnknownTask(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: greet
    description: greet
    state: todo
    run:
      kind: shell
      command: ["echo", "hi"]
`)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.no-such-task",
	)
	if err == nil {
		t.Fatal("expected error on unknown task id")
	}
	if !strings.Contains(err.Error(), "no task with id") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunStartFromTaskTaskWithoutRecipe(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: discuss
    description: a non-runnable task
    state: todo
`)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.discuss",
	)
	if err == nil {
		t.Fatal("expected error on task without recipe")
	}
	if !strings.Contains(err.Error(), "no `run` recipe") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunStartFromTaskMutexWithShell(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.greet",
		"--shell", "echo hi",
	)
	if err == nil {
		t.Fatal("expected mutex error")
	}
	// cobra phrases mutex errors as "if any flags in the group ... are set none of the others can be:".
	if !strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunListSpecRefFilter(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: greet
    description: greet
    state: todo
    references: [X.1]
    run:
      kind: shell
      command: ["echo", "hi"]
`)

	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.greet",
		"--run-id", "tagged",
	); err != nil {
		t.Fatalf("run start tagged: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo plain",
		"--run-id", "untagged",
	); err != nil {
		t.Fatalf("run start untagged: %v", err)
	}

	out, err := executeCommand(t, "run", "list",
		"--workspace", dir,
		"--spec-ref", "recipes.X.1",
	)
	if err != nil {
		t.Fatalf("run list --spec-ref: %v\n%s", err, out)
	}
	if !strings.Contains(out, "tagged") {
		t.Fatalf("expected tagged run in filtered list: %s", out)
	}
	if strings.Contains(out, "untagged") {
		t.Fatalf("untagged run should be filtered out: %s", out)
	}
}

func TestRunListFromTaskFilter(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	writeRecipeSpec(t, dir, "recipes", `
spec_version: 1
metadata:
  id: recipes
  name: Recipe demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: a
    description: a
    state: todo
    run:
      kind: shell
      command: ["echo", "a"]
  - id: b
    description: b
    state: todo
    run:
      kind: shell
      command: ["echo", "b"]
`)

	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.a",
		"--run-id", "ra",
	); err != nil {
		t.Fatalf("run start a: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--from-task", "recipes.b",
		"--run-id", "rb",
	); err != nil {
		t.Fatalf("run start b: %v", err)
	}

	out, err := executeCommand(t, "run", "list",
		"--workspace", dir,
		"--from-task", "recipes.a",
	)
	if err != nil {
		t.Fatalf("run list --from-task: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ra") {
		t.Fatalf("expected ra in filtered list: %s", out)
	}
	if strings.Contains(out, "rb") {
		t.Fatalf("rb should be filtered out: %s", out)
	}
}

func TestSplitTaskRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in       string
		spec     string
		task     string
		expectOK bool
	}{
		{"spec-format.define-run-recipes", "spec-format", "define-run-recipes", true},
		{"a.b", "a", "b", true},
		// Multi-segment task id (allows future task-id forms)
		{"foo.bar.baz", "foo", "bar.baz", true},
		{"no-dot", "", "", false},
		{".starts-with-dot", "", "", false},
		{"ends-with-dot.", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		spec, task, ok := splitTaskRef(tc.in)
		if ok != tc.expectOK {
			t.Errorf("%q: ok=%v want %v", tc.in, ok, tc.expectOK)
			continue
		}
		if !ok {
			continue
		}
		if spec != tc.spec || task != tc.task {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", tc.in, spec, task, tc.spec, tc.task)
		}
	}
}

func TestQualifyTaskRefs(t *testing.T) {
	t.Parallel()

	got := qualifyTaskRefs("specA", []string{"X.1", "specB.Y.2", "Z.1.1"})
	want := []string{"specA.X.1", "specB.Y.2", "specA.Z.1.1"}
	if len(got) != len(want) {
		t.Fatalf("len: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}
