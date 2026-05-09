package specfmt

import (
	"strings"
	"testing"
	"time"
)

const fixtureTemplate = `
spec_version: 1
metadata:
  id: api-template
  name: API spec template
  state: active
description: |
  Template for API specs.
tasks:
  - id: design
    description: Sketch the API surface
    state: todo
    references: [API.1]
components:
  API:
    name: API surface
    requirements:
      "1": Define the resource hierarchy
extra:
  template: true
  applies_to: workspace
  required_extra: [owner]
  related_specs: [overview]
  notes: |
    Keep API specs lean.
`

func parseDoc(t *testing.T, src string) *Document {
	t.Helper()
	doc, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestIsTemplateRecognizesBoolTrue(t *testing.T) {
	t.Parallel()

	doc := parseDoc(t, fixtureTemplate)
	if !IsTemplate(doc) {
		t.Fatal("template fixture should be recognized")
	}
	plain := parseDoc(t, `
spec_version: 1
metadata: {id: x, name: X, state: draft}
`)
	if IsTemplate(plain) {
		t.Fatal("plain spec must not be a template")
	}
}

func TestRequiredExtraKeysExtraction(t *testing.T) {
	t.Parallel()

	doc := parseDoc(t, fixtureTemplate)
	got := requiredExtraKeys(doc)
	if len(got) != 1 || got[0] != "owner" {
		t.Fatalf("required_extra: got %v want [owner]", got)
	}
}

func TestNewSpecFromTemplateInheritsShape(t *testing.T) {
	t.Parallel()

	tpl := parseDoc(t, fixtureTemplate)
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	got, err := NewSpecFromTemplate(ScaffoldOptions{
		ID:       "my-api",
		Template: tpl,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSpecFromTemplate: %v", err)
	}
	if got.Metadata.ID != "my-api" || got.Metadata.Name != "my-api" {
		t.Fatalf("metadata: %+v", got.Metadata)
	}
	if got.Metadata.State != "draft" {
		t.Fatalf("state: %q", got.Metadata.State)
	}
	if got.Metadata.CreatedAt != now.Format(time.RFC3339) {
		t.Fatalf("created_at: %q", got.Metadata.CreatedAt)
	}
	// Tasks/components inherited.
	if len(got.Tasks) != 1 || got.Tasks[0].ID != "design" {
		t.Fatalf("tasks not inherited: %v", got.Tasks)
	}
	if _, ok := got.Components["API"]; !ok {
		t.Fatal("API component not inherited")
	}
	// Template-marker keys stripped.
	if _, ok := got.Extra[ExtraTemplate]; ok {
		t.Fatal("scaffolded spec should not be a template")
	}
	if _, ok := got.Extra[ExtraAppliesTo]; ok {
		t.Fatal("applies_to should be stripped")
	}
	if _, ok := got.Extra[ExtraRequiredExtra]; ok {
		t.Fatal("required_extra should be stripped")
	}
	// Non-marker extras retained.
	if _, ok := got.Extra["related_specs"]; !ok {
		t.Fatal("related_specs should be retained")
	}
	// Back-reference to template set.
	if got.Extra[ExtraTemplateID] != "api-template" {
		t.Fatalf("template_id back-ref: %v", got.Extra[ExtraTemplateID])
	}
}

func TestNewSpecFromTemplateMinimalSkeleton(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	got, err := NewSpecFromTemplate(ScaffoldOptions{
		ID:  "skeleton",
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSpecFromTemplate: %v", err)
	}
	if got.Metadata.ID != "skeleton" {
		t.Fatalf("id: %q", got.Metadata.ID)
	}
	if got.Components != nil {
		t.Fatal("minimal skeleton should have nil components")
	}
}

// TestMinimalSkeletonYAMLContainsAllPlaceholders confirms the
// hand-rolled scaffold body emits every field a v1 spec can
// carry — not just the metadata block — so a new author sees
// the schema at the file level rather than discovering it
// through spec-format.yaml.
func TestMinimalSkeletonYAMLContainsAllPlaceholders(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	body, err := MinimalSkeletonYAML(ScaffoldOptions{
		ID:  "skeleton",
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("MinimalSkeletonYAML: %v", err)
	}
	got := string(body)

	// Every top-level key the schema understands must appear.
	for _, want := range []string{
		"spec_version: 1",
		"metadata:",
		"  id: skeleton",
		"  state: draft",
		"  owners: []",
		"  related_specs: []",
		"description: |",
		"tasks:",
		"  - id: example-task",
		"    state: todo",
		"    note: \"\"",
		"    proof: []",
		"    depends_on: []",
		"components:",
		"  EXAMPLE:",
		"constraints: {}",
		"extra:",
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in scaffold:\n%s", want, got)
		}
	}
	// Comments must survive — they're the whole point of the
	// hand-rolled emit path.
	for _, want := range []string{
		"# state: one of draft",
		"# proof:",
		"# depends_on:",
		"# components:",
	} {
		if !contains(got, want) {
			t.Errorf("missing comment %q", want)
		}
	}
}

// TestMinimalSkeletonYAMLParses confirms the emitted body
// round-trips through Parse — a malformed scaffold would be
// worse than a barebones one.
func TestMinimalSkeletonYAMLParses(t *testing.T) {
	t.Parallel()

	body, err := MinimalSkeletonYAML(ScaffoldOptions{ID: "round-trip"})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	doc, err := parseBytes(body)
	if err != nil {
		t.Fatalf("parse round-trip: %v\n%s", err, body)
	}
	if doc.Metadata.ID != "round-trip" {
		t.Fatalf("id round-trip: %q", doc.Metadata.ID)
	}
	if len(doc.Tasks) != 1 || doc.Tasks[0].ID != "example-task" {
		t.Fatalf("expected one example task, got %v", doc.Tasks)
	}
	if _, ok := doc.Components["EXAMPLE"]; !ok {
		t.Fatalf("expected EXAMPLE component, got %v", doc.Components)
	}
}

// TestMinimalSkeletonYAMLValidates confirms strict validate
// passes on the scaffold body. state: draft + example task
// state: todo means VAL.7 (done-needs-proof) and VAL.8 (blocked-
// needs-note) don't fire; the scaffold ships clean.
func TestMinimalSkeletonYAMLValidates(t *testing.T) {
	t.Parallel()

	body, err := MinimalSkeletonYAML(ScaffoldOptions{ID: "scaffolded"})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	doc, err := parseBytes(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res := Validate(doc, ModeStrict)
	if res.HasErrors() {
		t.Fatalf("scaffold should validate clean, got errors: %v", res.Errors())
	}
}

// contains is a tiny case-sensitive substring check; pulled in
// here so tests don't import strings just for one call site.
func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

func TestNewSpecFromTemplateRejectsBadID(t *testing.T) {
	t.Parallel()

	if _, err := NewSpecFromTemplate(ScaffoldOptions{ID: "Bad ID"}); err == nil {
		t.Fatal("expected kebab-case error")
	}
	if _, err := NewSpecFromTemplate(ScaffoldOptions{}); err == nil {
		t.Fatal("expected missing-id error")
	}
}

func TestWorkspaceTemplatesEnumerates(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	tpl := parseDoc(t, fixtureTemplate)
	tpl.Path = "/tmp/api-template.yaml"
	if err := w.Add(tpl); err != nil {
		t.Fatalf("Add template: %v", err)
	}
	plain := parseDoc(t, `
spec_version: 1
metadata: {id: plain, name: Plain, state: draft}
`)
	plain.Path = "/tmp/plain.yaml"
	if err := w.Add(plain); err != nil {
		t.Fatalf("Add plain: %v", err)
	}

	tps := w.Templates()
	if len(tps) != 1 {
		t.Fatalf("Templates: %v", tps)
	}
	if got := w.Template("api-template"); got == nil {
		t.Fatal("Template lookup missed")
	}
	if got := w.Template("plain"); got != nil {
		t.Fatal("Template lookup picked up a non-template")
	}
}
