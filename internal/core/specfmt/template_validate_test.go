package specfmt

import (
	"path/filepath"
	"strings"
	"testing"
)

const requiringTemplate = `
spec_version: 1
metadata:
  id: api-template
  name: API spec template
  state: active
extra:
  template: true
  applies_to: workspace
  required_extra: [owner, ticket]
`

const conformingSpec = `
spec_version: 1
metadata:
  id: conforming
  name: Conforming
  state: draft
extra:
  template_id: api-template
  owner: alice
  ticket: REX-1
`

const nonconformingSpec = `
spec_version: 1
metadata:
  id: nonconforming
  name: Nonconforming
  state: draft
extra:
  template_id: api-template
  owner: alice
`

func newTestWorkspace(t *testing.T, docs ...*Document) *Workspace {
	t.Helper()
	w := NewWorkspace()
	for i, d := range docs {
		if d.Path == "" {
			d.Path = "/tmp/spec-" + d.Metadata.ID + ".yaml"
		}
		if err := w.Add(d); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	return w
}

func TestValidateWorkspaceConformingSpecPasses(t *testing.T) {
	t.Parallel()

	w := newTestWorkspace(t, parseDoc(t, requiringTemplate), parseDoc(t, conformingSpec))
	res := ValidateWorkspace(w, ModeStrict)
	if res.HasErrors() {
		for _, e := range res.Errors() {
			t.Errorf("unexpected: %s", e)
		}
		t.Fatalf("conforming spec should validate: %d errors", len(res.Errors()))
	}
}

func TestValidateWorkspaceMissingRequiredExtraErrors(t *testing.T) {
	t.Parallel()

	w := newTestWorkspace(t, parseDoc(t, requiringTemplate), parseDoc(t, nonconformingSpec))
	res := ValidateWorkspace(w, ModeStrict)
	want := false
	for _, e := range res.Errors() {
		if e.Category == "template-required-extra" && strings.Contains(e.Path, "extra.ticket") {
			want = true
			break
		}
	}
	if !want {
		t.Fatalf("expected template-required-extra error for extra.ticket: %v", res.Issues)
	}
}

func TestValidateWorkspaceLenientDowngradesTemplateError(t *testing.T) {
	t.Parallel()

	w := newTestWorkspace(t, parseDoc(t, requiringTemplate), parseDoc(t, nonconformingSpec))
	res := ValidateWorkspace(w, ModeLenient)
	if res.HasErrors() {
		t.Fatalf("lenient should not error: %v", res.Errors())
	}
	want := false
	for _, w := range res.Warnings() {
		if w.Category == "template-required-extra" {
			want = true
			break
		}
	}
	if !want {
		t.Fatalf("expected template-required-extra warning: %v", res.Issues)
	}
}

func TestValidateWorkspaceTemplateOptedInExplicitWins(t *testing.T) {
	t.Parallel()

	// Two templates; the spec opts into the first via
	// extra.template_id. The workspace's default points at the
	// second. The explicit opt-in wins (TMPL.4).
	tplA := parseDoc(t, `
spec_version: 1
metadata: {id: tpl-a, name: A, state: active}
extra:
  template: true
  required_extra: [owner]
`)
	tplB := parseDoc(t, `
spec_version: 1
metadata: {id: tpl-b, name: B, state: active}
extra:
  template: true
  required_extra: [ticket]
`)
	target := parseDoc(t, `
spec_version: 1
metadata: {id: target, name: T, state: draft}
extra:
  template_id: tpl-a
  owner: alice
`)
	w := newTestWorkspace(t, tplA, tplB, target)
	w.SetDefaultTemplateID("tpl-b")
	res := ValidateWorkspace(w, ModeStrict)
	for _, e := range res.Errors() {
		if e.Category == "template-required-extra" {
			t.Fatalf("explicit template_id should win; got error from default-template path: %v", e)
		}
	}
}

func TestValidateWorkspaceWorkspaceDefaultAppliesWhenNoOptIn(t *testing.T) {
	t.Parallel()

	tpl := parseDoc(t, `
spec_version: 1
metadata: {id: workspace-default, name: D, state: active}
extra:
  template: true
  required_extra: [owner]
`)
	target := parseDoc(t, `
spec_version: 1
metadata: {id: target, name: T, state: draft}
`)
	w := newTestWorkspace(t, tpl, target)
	w.SetDefaultTemplateID("workspace-default")
	res := ValidateWorkspace(w, ModeStrict)

	want := false
	for _, e := range res.Errors() {
		if e.Category == "template-required-extra" && strings.Contains(e.Path, "extra.owner") {
			want = true
			break
		}
	}
	if !want {
		t.Fatalf("expected workspace-default-template enforcement: %v", res.Issues)
	}
}

func TestValidateWorkspaceUnknownTemplateIDIgnored(t *testing.T) {
	t.Parallel()

	target := parseDoc(t, `
spec_version: 1
metadata: {id: target, name: T, state: draft}
extra:
  template_id: ghost
`)
	w := newTestWorkspace(t, target)
	res := ValidateWorkspace(w, ModeStrict)

	for _, e := range res.Errors() {
		if e.Category == "template-required-extra" {
			t.Fatalf("unknown template should not produce template errors: %v", e)
		}
	}
}

func TestValidateWorkspaceTemplateItselfIsNotValidatedAgainstSelf(t *testing.T) {
	t.Parallel()

	tpl := parseDoc(t, `
spec_version: 1
metadata: {id: api-template, name: T, state: active}
extra:
  template: true
  required_extra: [owner]
`)
	w := newTestWorkspace(t, tpl)
	res := ValidateWorkspace(w, ModeStrict)
	for _, e := range res.Errors() {
		if e.Category == "template-required-extra" {
			t.Fatalf("template should not be validated against itself: %v", e)
		}
	}
}

func TestValidateWorkspaceRealReposUnaffected(t *testing.T) {
	t.Parallel()

	// Existing canary: every checked-in spec must still pass strict
	// validation when no template is configured. The new pass adds
	// no errors when the workspace has no templates.
	w := loadRealWorkspaceForTemplateTest(t)
	res := ValidateWorkspace(w, ModeStrict)
	if res.HasErrors() {
		for _, e := range res.Errors() {
			t.Errorf("regression: %s", e)
		}
		t.Fatalf("real specs should still pass strict validation")
	}
}

// loadRealWorkspaceForTemplateTest mirrors the helper in
// resolver_test.go; redefined here so this file is independently
// runnable.
func loadRealWorkspaceForTemplateTest(t *testing.T) *Workspace {
	t.Helper()
	w := NewWorkspace()
	matches, err := filepath.Glob(filepath.Join(repoSpecsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, p := range matches {
		doc, err := ParseFile(p)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", p, err)
		}
		if err := w.Add(doc); err != nil {
			t.Fatalf("Add %s: %v", p, err)
		}
	}
	return w
}
