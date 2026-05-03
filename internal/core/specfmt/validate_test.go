package specfmt

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRealSpecsPassValidator is the canary test for build-order step 3:
// every checked-in spec under specs/ must pass strict validation.
// If this fails, either the spec is wrong or the validator is.
func TestRealSpecsPassValidator(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(filepath.Join(repoSpecsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc, err := ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			res := Validate(doc, ModeStrict)
			if res.HasErrors() {
				for _, issue := range res.Errors() {
					t.Errorf("%s: %s", path, issue)
				}
				t.Fatalf("%s: %d errors", path, len(res.Errors()))
			}
		})
	}
}

func TestValidateRejectsMissingSpecVersion(t *testing.T) {
	t.Parallel()

	doc, err := Parse(strings.NewReader(`
metadata:
  id: x
  name: X
  state: draft
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "spec_version", "required-field") {
		t.Fatalf("expected spec_version required-field error: %v", res.Issues)
	}
}

func TestValidateRejectsBadMetadataState(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: x
  name: X
  state: in-flight
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "metadata.state", "format") {
		t.Fatalf("expected metadata.state format error: %v", res.Issues)
	}
}

func TestValidateRejectsBadCreatedAt(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata:
  id: x
  name: X
  state: draft
  created_at: not-a-date
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "metadata.created_at", "format") {
		t.Fatalf("expected metadata.created_at format error: %v", res.Issues)
	}
}

func TestValidateRejectsBadComponentID(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
components:
  bad-lower:
    name: Bad
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "components.bad-lower", "format") {
		t.Fatalf("expected component id format error: %v", res.Issues)
	}
}

func TestValidateAcceptsHyphenatedComponentID(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
components:
  EXEC-SCOPE:
    name: Exec scope
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if res.HasErrors() {
		t.Fatalf("hyphenated component id rejected: %v", res.Issues)
	}
}

func TestValidateRejectsConstraintMissingDescription(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
constraints:
  ENG:
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "constraints.ENG.description", "required-field") {
		t.Fatalf("expected constraint description required-field error: %v", res.Issues)
	}
}

func TestValidateRejectsCollidingComponentAndConstraint(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
components:
  AUTH:
    name: Auth
    requirements:
      "1": one
constraints:
  AUTH:
    description: also auth
    requirements:
      "1": one
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "constraints.AUTH", "collision") {
		t.Fatalf("expected collision error: %v", res.Issues)
	}
}

func TestValidateRejectsBadTaskState(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: a
    description: do thing
    state: pending
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].state", "format") {
		t.Fatalf("expected task state format error: %v", res.Issues)
	}
}

func TestValidateRejectsDuplicateTaskID(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: a
    description: first
    state: todo
  - id: a
    description: second
    state: todo
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[1].id", "duplicate") {
		t.Fatalf("expected duplicate task id error: %v", res.Issues)
	}
}

func TestValidateRejectsMalformedACIDInTaskReferences(t *testing.T) {
	t.Parallel()

	doc, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: x, name: X, state: draft}
tasks:
  - id: a
    description: do thing
    state: todo
    references: [SYS.1, lower.x.1]
`))
	res := Validate(doc, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].references[1]", "acid") {
		t.Fatalf("expected ACID format error on second ref: %v", res.Issues)
	}
	// First ref is short-form valid; should not error.
	for _, e := range res.Errors() {
		if e.Path == "tasks[0].references[0]" {
			t.Fatalf("first ref should be valid short form, got: %v", e)
		}
	}
}

func TestValidateUnknownTopLevelKeyStrictVsLenient(t *testing.T) {
	t.Parallel()

	src := `
spec_version: 1
metadata: {id: x, name: X, state: draft}
weird_key: hello
`
	doc, _ := Parse(strings.NewReader(src))

	strict := Validate(doc, ModeStrict)
	if !hasIssue(strict.Errors(), "weird_key", "unknown-key") {
		t.Fatalf("strict mode should error: %v", strict.Issues)
	}

	lenient := Validate(doc, ModeLenient)
	if lenient.HasErrors() {
		t.Fatalf("lenient mode should not error: %v", lenient.Errors())
	}
	if !hasIssue(lenient.Warnings(), "weird_key", "unknown-key") {
		t.Fatalf("lenient mode should warn: %v", lenient.Issues)
	}
}

func TestValidateIsIdempotent(t *testing.T) {
	t.Parallel()

	// VAL.4: validation must not modify the document.
	doc, _ := ParseFile(filepath.Join(repoSpecsDir, "spec-format.yaml"))
	first := Validate(doc, ModeStrict)
	second := Validate(doc, ModeStrict)
	if len(first.Issues) != len(second.Issues) {
		t.Fatalf("issue count drift: first=%d second=%d", len(first.Issues), len(second.Issues))
	}
}

func TestSortIssues(t *testing.T) {
	t.Parallel()

	in := []Issue{
		{File: "b.yaml", Path: "metadata.id"},
		{File: "a.yaml", Path: "tasks[1].id"},
		{File: "a.yaml", Path: "metadata.id", Severity: SeverityWarning},
		{File: "a.yaml", Path: "metadata.id", Severity: SeverityError},
	}
	got := SortIssues(in)
	want := []Issue{
		{File: "a.yaml", Path: "metadata.id", Severity: SeverityError},
		{File: "a.yaml", Path: "metadata.id", Severity: SeverityWarning},
		{File: "a.yaml", Path: "tasks[1].id"},
		{File: "b.yaml", Path: "metadata.id"},
	}
	for i := range got {
		if got[i].File != want[i].File || got[i].Path != want[i].Path || got[i].Severity != want[i].Severity {
			t.Fatalf("at %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func hasIssue(issues []Issue, path, category string) bool {
	for _, i := range issues {
		if i.Path == path && i.Category == category {
			return true
		}
	}
	return false
}
