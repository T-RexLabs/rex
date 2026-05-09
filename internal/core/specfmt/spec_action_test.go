package specfmt

import (
	"strings"
	"testing"
)

// TestValidateSpecActionRecipeAcceptsHappyShape covers the
// successful path: action enum + target kebab + harness +
// prompt all present.
func TestValidateSpecActionRecipeAcceptsHappyShape(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: rewrite
    description: rewrite this spec
    state: todo
    run:
      kind: spec_action
      action: amend
      target: x
      harness: claude-code
      prompt: |
        Tighten the existing requirements
`))
	res := Validate(doc, ModeStrict)
	if res.HasErrors() {
		t.Fatalf("expected clean: %v", res.Errors())
	}
}

// TestValidateSpecActionRejectsUnknownAction covers RECIPE.6.1's
// action enum.
func TestValidateSpecActionRejectsUnknownAction(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: rewrite
    description: rewrite this spec
    state: todo
    run:
      kind: spec_action
      action: yeet
      target: x
      harness: claude-code
      prompt: do a thing
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.action", "format") {
		t.Fatalf("expected action enum error: %v", res.Issues)
	}
}

// TestValidateSpecActionRequiresTarget covers RECIPE.6.2's
// required-field check.
func TestValidateSpecActionRequiresTarget(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: rewrite
    description: rewrite this spec
    state: todo
    run:
      kind: spec_action
      action: amend
      harness: claude-code
      prompt: do a thing
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.target", "required-field") {
		t.Fatalf("expected target-required error: %v", res.Issues)
	}
}

// TestValidateSpecActionRejectsNonKebabTarget covers RECIPE.6.2's
// shape check (target must be kebab-case).
func TestValidateSpecActionRejectsNonKebabTarget(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: rewrite
    description: rewrite this spec
    state: todo
    run:
      kind: spec_action
      action: amend
      target: NotKebab
      harness: claude-code
      prompt: do a thing
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].run.target", "format") {
		t.Fatalf("expected target-format error: %v", res.Issues)
	}
}

// TestValidateSpecActionRejectsCrossKindFields confirms shell-
// only / harness-only fields aren't allowed alongside spec_action.
func TestValidateSpecActionRejectsCrossKindFields(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: rewrite
    description: rewrite
    state: todo
    run:
      kind: spec_action
      action: amend
      target: x
      harness: claude-code
      prompt: hello
      command: ["echo", "no"]
      cwd: ./elsewhere
`))
	res := Validate(doc, ModeStrict)
	gotCommand := false
	gotCwd := false
	for _, iss := range res.Errors() {
		if iss.Path == "tasks[0].run.command" {
			gotCommand = true
		}
		if iss.Path == "tasks[0].run.cwd" {
			gotCwd = true
		}
	}
	if !gotCommand || !gotCwd {
		t.Fatalf("expected reject errors for command + cwd, got %v", res.Errors())
	}
}

// TestValidateWorkspaceFlagsDanglingSpecActionTarget exercises
// the cross-spec resolution check (RECIPE.6.2 second pass).
func TestValidateWorkspaceFlagsDanglingSpecActionTarget(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: rewrite
    description: rewrite
    state: todo
    run:
      kind: spec_action
      action: amend
      target: ghost
      harness: claude-code
      prompt: hello
`))
	if err := w.Add(doc); err != nil {
		t.Fatalf("Add: %v", err)
	}
	res := ValidateWorkspace(w, ModeStrict)
	found := false
	for _, iss := range res.Errors() {
		if iss.Category == "dangling-spec-action-target" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected dangling-spec-action-target error, got %v", res.Errors())
	}
}
