package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpec drops a spec into the workspace's specs/ tree so
// LoadFromTaskRef has something to read.
func writeSpec(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
}

// TestLoadFromTaskRefSpecActionPrependsTargetBody covers the
// load-time behaviour of RECIPE.6: the resolver reads the
// target spec's YAML and prepends it to the rendered prompt
// with action + preamble + author instruction. The harness
// sees the full file before being asked to act on it.
func TestLoadFromTaskRefSpecActionPrependsTargetBody(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// The target spec — content the harness will see.
	writeSpec(t, root, "subject", `spec_version: 1
metadata: {id: subject, name: Subject, state: draft}
description: |
  TODO subject body.
`)
	// The recipe-bearing spec — declares a task whose run is
	// kind: spec_action targeting "subject".
	writeSpec(t, root, "host", `spec_version: 1
metadata: {id: host, name: Host, state: draft}
tasks:
  - id: amend-subject
    description: tighten the subject spec
    state: todo
    run:
      kind: spec_action
      action: amend
      target: subject
      harness: claude-code
      prompt: |
        Rewrite description so it explains what subject owns.
`)

	out, err := LoadFromTaskRef(root, "host.amend-subject", nil)
	if err != nil {
		t.Fatalf("LoadFromTaskRef: %v", err)
	}
	if out.Recipe.Kind != "spec_action" {
		t.Fatalf("kind: %q", out.Recipe.Kind)
	}
	for _, want := range []string{
		"Action requested: amend",
		"The spec's current content:",
		"id: subject",
		"TODO subject body.",
		"Author's instruction:",
		"Rewrite description so it explains what subject owns.",
		"specs/_proposed/subject-amendment-",
	} {
		if !strings.Contains(out.Prompt, want) {
			t.Errorf("missing %q in rendered prompt:\n%s", want, out.Prompt)
		}
	}
}

// TestLoadFromTaskRefSpecActionFoldsTargetIntoSpecRefs covers
// the bidirectional-provenance bit of RECIPE.6: the run's
// SpecRefs include the target so /specs/<target> surfaces the
// run alongside any other citations.
func TestLoadFromTaskRefSpecActionFoldsTargetIntoSpecRefs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSpec(t, root, "subject", `spec_version: 1
metadata: {id: subject, name: Subject, state: draft}
`)
	writeSpec(t, root, "host", `spec_version: 1
metadata: {id: host, name: Host, state: draft}
tasks:
  - id: review-subject
    description: review
    state: todo
    references: [host.X.1]
    run:
      kind: spec_action
      action: review
      target: subject
      harness: claude-code
      prompt: review please
`)

	out, err := LoadFromTaskRef(root, "host.review-subject", nil)
	if err != nil {
		t.Fatalf("LoadFromTaskRef: %v", err)
	}
	want := []string{"host.X.1", "subject"}
	if !sliceEq(out.SpecRefs, want) {
		t.Fatalf("SpecRefs = %v, want %v", out.SpecRefs, want)
	}
}

// TestLoadFromTaskRefSpecActionRejectsMissingTarget covers
// the load-time error when the target file doesn't exist —
// e.g. a spec was deleted but the recipe still cites it.
func TestLoadFromTaskRefSpecActionRejectsMissingTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSpec(t, root, "host", `spec_version: 1
metadata: {id: host, name: Host, state: draft}
tasks:
  - id: amend-ghost
    description: ghost
    state: todo
    run:
      kind: spec_action
      action: amend
      target: ghost
      harness: claude-code
      prompt: do a thing
`)
	_, err := LoadFromTaskRef(root, "host.amend-ghost", nil)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected target-missing error; got %v", err)
	}
}

// TestSpecActionPreambleVariesByAction confirms each action's
// closing instruction matches its purpose: amend → amendment
// path, draft → new spec, review → Markdown review.
func TestSpecActionPreambleVariesByAction(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"amend":  "specs/_proposed/subject-amendment-",
		"draft":  "Output a complete spec body.",
		"review": "Provide a Markdown review",
	}
	for action, snippet := range cases {
		root := t.TempDir()
		writeSpec(t, root, "subject", `spec_version: 1
metadata: {id: subject, name: Subject, state: draft}
`)
		writeSpec(t, root, "host", `spec_version: 1
metadata: {id: host, name: Host, state: draft}
tasks:
  - id: act
    description: act
    state: todo
    run:
      kind: spec_action
      action: `+action+`
      target: subject
      harness: claude-code
      prompt: please do
`)
		out, err := LoadFromTaskRef(root, "host.act", nil)
		if err != nil {
			t.Fatalf("action=%s LoadFromTaskRef: %v", action, err)
		}
		if !strings.Contains(out.Prompt, snippet) {
			t.Errorf("action=%s missing %q in prompt:\n%s", action, snippet, out.Prompt)
		}
	}
}

func sliceEq(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
