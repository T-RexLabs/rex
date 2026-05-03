package specfmt

import (
	"path/filepath"
	"strings"
	"testing"
)

func loadRealWorkspace(t *testing.T) *Workspace {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repoSpecsDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	w := NewWorkspace()
	for _, path := range matches {
		doc, err := ParseFile(path)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", path, err)
		}
		if err := w.Add(doc); err != nil {
			t.Fatalf("Add %s: %v", path, err)
		}
	}
	return w
}

func TestWorkspaceAddRejectsDuplicates(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	doc, err := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: dup, name: D, state: draft}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := w.Add(doc); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := w.Add(doc); err == nil {
		t.Fatal("second Add: expected duplicate error")
	}
}

func TestWorkspaceAddRejectsEmptyID(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	if err := w.Add(&Document{}); err == nil {
		t.Fatal("expected empty-id error")
	}
}

func TestResolveShortFormHits(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	ref, err := ParseACID("SYS.1")
	if err != nil {
		t.Fatalf("ParseACID: %v", err)
	}
	res := w.Resolve(ref, "overview")
	if !res.Found {
		t.Fatalf("expected hit: %+v", res)
	}
	if res.Doc.Metadata.ID != "overview" || res.GroupID != "SYS" {
		t.Fatalf("resolution: %+v", res)
	}
	if !res.InComponent {
		t.Fatal("SYS lives in components, not constraints")
	}
}

func TestResolveShortFormResolvesToConstraint(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	ref, _ := ParseACID("ENG.1")
	res := w.Resolve(ref, "overview")
	if !res.Found {
		t.Fatalf("expected hit: %+v", res)
	}
	if res.InComponent {
		t.Fatal("ENG lives in constraints, not components")
	}
	if res.GroupID != "ENG" {
		t.Fatalf("group id: %q", res.GroupID)
	}
}

func TestResolveFullFormCrossSpec(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	ref, _ := ParseACID("storage.EVENTS.3")
	// fromSpecID irrelevant for full-form refs.
	res := w.Resolve(ref, "execution")
	if !res.Found {
		t.Fatalf("cross-spec full form: %+v", res)
	}
	if res.Doc.Metadata.ID != "storage" {
		t.Fatalf("cross-spec doc: %q", res.Doc.Metadata.ID)
	}
}

func TestResolveDanglingShortForm(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	ref, _ := ParseACID("SYS.999")
	res := w.Resolve(ref, "overview")
	if res.Found {
		t.Fatalf("expected miss: %+v", res)
	}
	if res.FullACID != "overview.SYS.999" {
		t.Fatalf("full acid: %q", res.FullACID)
	}
}

func TestResolveDanglingUnknownSpec(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	ref, _ := ParseACID("ghost-spec.AUTH.1")
	res := w.Resolve(ref, "overview")
	if res.Found {
		t.Fatalf("expected miss: %+v", res)
	}
}

func TestResolveShortFormWithoutFromSpec(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	ref, _ := ParseACID("SYS.1")
	res := w.Resolve(ref, "")
	if res.Found {
		t.Fatalf("short form with no fromSpecID must not silently match: %+v", res)
	}
}

// TestValidateWorkspaceCleanForRealSpecs is the canary test for the
// resolver: every checked-in spec's task references must resolve to
// an existing requirement. If a spec is added that cites a dangling
// ACID, this test surfaces it.
func TestValidateWorkspaceCleanForRealSpecs(t *testing.T) {
	t.Parallel()

	w := loadRealWorkspace(t)
	res := ValidateWorkspace(w, ModeStrict)
	if res.HasErrors() {
		for _, issue := range res.Errors() {
			t.Errorf("%s", issue)
		}
		t.Fatalf("%d cross-spec validation errors", len(res.Errors()))
	}
}

func TestValidateWorkspaceFlagsDanglingShortForm(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	host, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: host, name: H, state: draft}
tasks:
  - id: a
    description: cites a missing local req
    state: todo
    references: [GHOST.1]
components:
  REAL:
    name: Real
    requirements:
      "1": exists
`))
	host.Path = "host.yaml"
	if err := w.Add(host); err != nil {
		t.Fatalf("Add: %v", err)
	}
	res := ValidateWorkspace(w, ModeStrict)
	if !hasIssue(res.Errors(), "tasks[0].references[0]", "dangling-acid") {
		t.Fatalf("expected dangling-acid error: %v", res.Issues)
	}
}

func TestValidateWorkspaceFlagsDanglingFullForm(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	host, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: host, name: H, state: draft}
tasks:
  - id: a
    description: cites cross-spec missing req
    state: todo
    references: [missing-spec.X.1]
`))
	host.Path = "host.yaml"
	_ = w.Add(host)
	res := ValidateWorkspace(w, ModeStrict)
	errs := res.Errors()
	if !hasIssue(errs, "tasks[0].references[0]", "dangling-acid") {
		t.Fatalf("expected dangling-acid error: %v", res.Issues)
	}
}

func TestValidateWorkspaceLenientDowngradesDangling(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	host, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: host, name: H, state: draft}
tasks:
  - id: a
    description: cites missing
    state: todo
    references: [GHOST.1]
`))
	_ = w.Add(host)
	res := ValidateWorkspace(w, ModeLenient)
	if res.HasErrors() {
		t.Fatalf("lenient mode should warn, not error: %v", res.Errors())
	}
	if !hasIssue(res.Warnings(), "tasks[0].references[0]", "dangling-acid") {
		t.Fatalf("expected dangling-acid warning: %v", res.Issues)
	}
}

func TestValidateWorkspaceCarriesFile(t *testing.T) {
	t.Parallel()

	w := NewWorkspace()
	host, _ := Parse(strings.NewReader(`
spec_version: 1
metadata: {id: host, name: H, state: bogus}
`))
	host.Path = "/tmp/host.yaml"
	_ = w.Add(host)
	res := ValidateWorkspace(w, ModeStrict)
	for _, issue := range res.Errors() {
		if issue.File != "/tmp/host.yaml" {
			t.Fatalf("File not stamped: %+v", issue)
		}
	}
}
