package specfmt

import (
	"strings"
	"testing"
)

// recipeDocSrc returns a minimal-valid spec wrapping the supplied
// task body. Keeps the per-test YAML free of boilerplate.
func recipeDocSrc(taskBody string) string {
	return `
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: ` + "`t1`" + `
    description: a task
    state: todo
` + taskBody
}

func TestRecipeShellValid(t *testing.T) {
	t.Parallel()

	doc, err := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: shell
      command: ["go", "test", "./..."]
      cwd: ./
      env:
        FOO: bar
components:
  X:
    name: X
    requirements:
      "1": one
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := doc.Tasks[0].Run.Kind; got != RecipeKindShell {
		t.Fatalf("kind: got %q", got)
	}
	if got := doc.Tasks[0].Run.Command; len(got) != 3 || got[0] != "go" {
		t.Fatalf("command: got %v", got)
	}

	res := Validate(doc, ModeStrict)
	if res.HasErrors() {
		t.Fatalf("expected no errors: %v", res.Issues)
	}
}

func TestRecipeShellMissingCommand(t *testing.T) {
	t.Parallel()

	doc, err := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: shell
components:
  X:
    name: X
    requirements:
      "1": one
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.command", "required-field") {
		t.Fatalf("expected required-field on command: %v", res.Issues)
	}
}

func TestRecipeShellRejectsForeignFields(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: shell
      command: ["echo", "hi"]
      harness: claude-code
      prompt: should not be here
components:
  X:
    name: X
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.harness", "format") {
		t.Fatalf("expected format error on harness: %v", res.Issues)
	}
	if !hasIssue(res.Errors(), "tasks[0].run.prompt", "format") {
		t.Fatalf("expected format error on prompt: %v", res.Issues)
	}
}

func TestRecipeHarnessValid(t *testing.T) {
	t.Parallel()

	doc, err := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: harness
      harness: claude-code
      prompt: |
        Do task {{task.id}} from spec {{spec.id}}.
        Description: {{task.description}}
        Refs: {{task.references}}
      permission_scope: workspace
components:
  X:
    name: X
    requirements:
      "1": one
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := doc.Tasks[0].Run.PermissionScope; got != PermissionScopeWorkspace {
		t.Fatalf("permission_scope: got %q", got)
	}
	res := Validate(doc, ModeStrict)
	if res.HasErrors() {
		t.Fatalf("expected no errors: %v", res.Issues)
	}
}

func TestRecipeHarnessRequiresHarnessAndPrompt(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: harness
components:
  X:
    name: X
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.harness", "required-field") {
		t.Fatalf("expected harness required-field: %v", res.Issues)
	}
	if !hasIssue(res.Errors(), "tasks[0].run.prompt", "required-field") {
		t.Fatalf("expected prompt required-field: %v", res.Issues)
	}
}

func TestRecipeHarnessBadPermissionScope(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: harness
      harness: claude-code
      prompt: hello
      permission_scope: god_mode
components:
  X:
    name: X
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.permission_scope", "format") {
		t.Fatalf("expected permission_scope format error: %v", res.Issues)
	}
}

func TestRecipeUnknownKindStrictVsLenient(t *testing.T) {
	t.Parallel()

	src := `
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: harnes
      harness: claude-code
      prompt: hello
components:
  X:
    name: X
    requirements:
      "1": one
`
	doc, _ := Parse(strings.NewReader(src))
	strict := Validate(doc, ModeStrict)
	if !hasIssue(strict.Errors(), "tasks[0].run.kind", "format") {
		t.Fatalf("strict: expected error on bad kind: %v", strict.Issues)
	}
	lenient := Validate(doc, ModeLenient)
	if lenient.HasErrors() {
		t.Fatalf("lenient: did not expect errors: %v", lenient.Errors())
	}
	if !hasIssue(lenient.Warnings(), "tasks[0].run.kind", "format") {
		t.Fatalf("lenient: expected warning on bad kind: %v", lenient.Issues)
	}
}

func TestRecipePromptTokenStrictVsLenient(t *testing.T) {
	t.Parallel()

	src := `
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    run:
      kind: harness
      harness: claude-code
      prompt: "Hello {{nope.token}} world {{task.id}}"
components:
  X:
    name: X
    requirements:
      "1": one
`
	doc, _ := Parse(strings.NewReader(src))
	strict := Validate(doc, ModeStrict)
	if !hasIssue(strict.Errors(), "tasks[0].run.prompt", "format") {
		t.Fatalf("strict: expected error on unknown token: %v", strict.Issues)
	}
	// Known token must not error.
	for _, e := range strict.Errors() {
		if strings.Contains(e.Message, "task.id") {
			t.Fatalf("known token task.id flagged: %v", e)
		}
	}
	lenient := Validate(doc, ModeLenient)
	if lenient.HasErrors() {
		t.Fatalf("lenient: did not expect errors: %v", lenient.Errors())
	}
	if !hasIssue(lenient.Warnings(), "tasks[0].run.prompt", "format") {
		t.Fatalf("lenient: expected warning on unknown token: %v", lenient.Issues)
	}
}

func TestRecipeSpecValidateDefaults(t *testing.T) {
	t.Parallel()

	doc, err := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: validate the specs
    state: todo
    run:
      kind: spec_validate
components:
  X:
    name: X
    requirements:
      "1": one
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := doc.Tasks[0].Run.StrictValue(); !got {
		t.Fatalf("StrictValue default: got %v want true", got)
	}
	if doc.Tasks[0].Run.Paths != nil {
		t.Fatalf("paths default: got %v", doc.Tasks[0].Run.Paths)
	}
	res := Validate(doc, ModeStrict)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %v", res.Issues)
	}
}

func TestRecipeShellShellTokenSubstitution(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
tasks:
  - id: t1
    description: a task
    state: todo
    references: [t.X.1]
    run:
      kind: shell
      command: ["echo", "{{task.id}} {{garbage}}"]
components:
  X:
    name: X
    requirements:
      "1": one
`))
	strict := Validate(doc, ModeStrict)
	if !hasIssue(strict.Errors(), "tasks[0].run.command[1]", "format") {
		t.Fatalf("strict: expected unknown-token error on command arg: %v", strict.Issues)
	}
}
