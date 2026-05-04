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
		ID:    "skeleton",
		Now:   func() time.Time { return now },
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
