package specfmt

import (
	"path/filepath"
	"strings"
	"testing"
)

// repoSpecsDir is the relative path from this package to the
// checked-in specs/ directory. Tests run with the package as
// CWD, so navigate up three levels.
const repoSpecsDir = "../../../specs"

func TestParseAllRealSpecs(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(filepath.Join(repoSpecsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) < 14 {
		t.Fatalf("found %d specs, expected at least 14", len(matches))
	}

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc, err := ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			if doc.SpecVersion != 1 {
				t.Fatalf("spec_version: got %d", doc.SpecVersion)
			}
			if doc.Metadata.ID == "" {
				t.Fatal("metadata.id missing")
			}
			if doc.Metadata.Name == "" {
				t.Fatal("metadata.name missing")
			}
			if doc.Metadata.State == "" {
				t.Fatal("metadata.state missing")
			}
		})
	}
}

func TestParseSpecFormatItself(t *testing.T) {
	t.Parallel()

	doc, err := ParseFile(filepath.Join(repoSpecsDir, "spec-format.yaml"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if doc.Metadata.ID != "spec-format" {
		t.Fatalf("id: got %q", doc.Metadata.ID)
	}

	// Component order should be the document's source order.
	wantOrder := []string{"CORE", "META", "DESC", "TASK", "COMP", "REQ", "ACID", "CONST", "EXTRA", "TMPL", "RECIPE", "PROMPT", "PROOF", "AMEND", "VAL"}
	gotOrder := doc.ComponentOrder()
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("component count: got %d (%v) want %d", len(gotOrder), gotOrder, len(wantOrder))
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("component order at %d: got %q want %q (%v)", i, gotOrder[i], wantOrder[i], gotOrder)
		}
	}

	// COMP block must contain the new 1.1 added by the amendment.
	comp, ok := doc.Components["COMP"]
	if !ok {
		t.Fatal("COMP component missing")
	}
	if _, ok := comp.Requirements["1.1"]; !ok {
		t.Fatalf("COMP.1.1 missing — amendment was not folded? requirements=%v", comp.RequirementOrder())
	}

	// CONST block must contain the new 3 added by the amendment.
	if _, ok := doc.Constraints["COMPAT"]; !ok {
		t.Fatal("constraints.COMPAT missing")
	}
	if _, ok := doc.Components["VAL"].Requirements["1"]; !ok {
		t.Fatal("VAL.1 missing")
	}
}

func TestRequirementShortFormPlainString(t *testing.T) {
	t.Parallel()

	src := `
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
components:
  X:
    name: X
    requirements:
      "1": A plain string requirement
`
	doc, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := doc.Components["X"].Requirements["1"]
	if r.Text != "A plain string requirement" {
		t.Fatalf("text: got %q", r.Text)
	}
	if r.Deprecated {
		t.Fatal("Deprecated should be false")
	}
}

func TestRequirementMappingForm(t *testing.T) {
	t.Parallel()

	src := `
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
components:
  X:
    name: X
    requirements:
      "1":
        text: Old wording
        deprecated: true
        replaced_by: t.X.2
        notes: superseded after audit
      "2": New wording
`
	doc, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r1 := doc.Components["X"].Requirements["1"]
	if r1.Text != "Old wording" || !r1.Deprecated || r1.ReplacedBy != "t.X.2" || r1.Notes == "" {
		t.Fatalf("mapping form not preserved: %+v", r1)
	}
	r2 := doc.Components["X"].Requirements["2"]
	if r2.Text != "New wording" || r2.Deprecated {
		t.Fatalf("plain form alongside mapping form: %+v", r2)
	}
}

func TestRequirementOrderPreserved(t *testing.T) {
	t.Parallel()

	src := `
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
      "1.1": one-one
      "2": two
      "2-note": two note
      "3": three
`
	doc, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"1", "1.1", "2", "2-note", "3"}
	got := doc.Components["X"].RequirementOrder()
	if len(got) != len(want) {
		t.Fatalf("length: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestParseRecordsTopLevelKeys(t *testing.T) {
	t.Parallel()

	src := `
spec_version: 1
metadata:
  id: t
  name: T
  state: draft
description: hello
tasks: []
components:
  X:
    name: X
    requirements:
      "1": one
unknown_key: should be visible to validator
`
	doc, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	keys := doc.TopLevelKeys()
	expected := map[string]bool{
		"spec_version": false,
		"metadata":     false,
		"description":  false,
		"tasks":        false,
		"components":   false,
		"unknown_key":  false,
	}
	for _, k := range keys {
		if _, ok := expected[k]; !ok {
			t.Fatalf("unexpected top-level key surfaced: %q", k)
		}
		expected[k] = true
	}
	for k, seen := range expected {
		if !seen {
			t.Fatalf("expected top-level key not surfaced: %q (got %v)", k, keys)
		}
	}
}

func TestParseRejectsInvalidYAML(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.NewReader("not: [valid"))
	if err == nil {
		t.Fatal("expected error on malformed YAML")
	}
}
