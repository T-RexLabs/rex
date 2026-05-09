package harnessbrief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedWorkspace builds the .rex/ skeleton + the supplied specs
// + an optional workspace.yaml so Build has something to read.
func seedWorkspace(t *testing.T, specs map[string]string, workspaceYAML string) string {
	t.Helper()
	root := t.TempDir()
	rexDir := filepath.Join(root, ".rex")
	if err := os.MkdirAll(filepath.Join(rexDir, "specs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if workspaceYAML != "" {
		if err := os.WriteFile(filepath.Join(rexDir, "workspace.yaml"), []byte(workspaceYAML), 0o644); err != nil {
			t.Fatalf("write workspace.yaml: %v", err)
		}
	}
	for id, body := range specs {
		if err := os.WriteFile(filepath.Join(rexDir, "specs", id+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write spec %s: %v", id, err)
		}
	}
	return root
}

// TestBuildIncludesWorkspaceMetaAndSpecs covers the happy
// path: workspace name + id from workspace.yaml, sorted spec
// list under "Active specs".
func TestBuildIncludesWorkspaceMetaAndSpecs(t *testing.T) {
	t.Parallel()

	root := seedWorkspace(t, map[string]string{
		"sync": `spec_version: 1
metadata: {id: sync, name: Sync, state: draft}
`,
		"audit": `spec_version: 1
metadata: {id: audit, name: Audit, state: active}
`,
	}, "id: ws-1\nname: My Workspace\n")

	out, err := Build(Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"# Rex workspace context",
		"My Workspace",
		"`ws-1`",
		"## Active specs (2)",
		"`audit`",
		"`sync`",
		"## Conventions",
		"## Useful commands",
		"rex spec validate",
		"rex spec ask",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in brief:\n%s", want, out)
		}
	}
	// Specs should render in alphabetical order — audit before
	// sync — so seek both indexes and compare.
	if i := strings.Index(out, "audit"); i >= 0 {
		if j := strings.Index(out[i:], "sync"); j < 0 {
			t.Errorf("expected audit before sync in spec list:\n%s", out)
		}
	}
}

// TestBuildRunSectionAppearsWhenFromTaskOrSpecRefsSet covers
// the per-run section: the brief calls out which task the run
// is targeting + which ACIDs it cites.
func TestBuildRunSectionAppearsWhenFromTaskOrSpecRefsSet(t *testing.T) {
	t.Parallel()

	root := seedWorkspace(t, nil, "id: ws-1\nname: WS\n")

	out, err := Build(Options{
		WorkspaceRoot: root,
		FromTask:      "execution.dag-primitives",
		SpecRefs:      []string{"execution.PRIM.6", "sync.ORDER.3"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"## Current run",
		"`execution.dag-primitives`",
		"`execution.PRIM.6`",
		"`sync.ORDER.3`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in brief:\n%s", want, out)
		}
	}
}

// TestBuildOmitsRunSectionWhenNothingSet ensures the section
// isn't rendered for ad-hoc runs that don't carry provenance.
func TestBuildOmitsRunSectionWhenNothingSet(t *testing.T) {
	t.Parallel()

	root := seedWorkspace(t, nil, "id: ws\nname: WS\n")
	out, err := Build(Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(out, "## Current run") {
		t.Fatalf("run section should be hidden when no FromTask / SpecRefs:\n%s", out)
	}
}

// TestBuildRespectsTemplateOverride proves the per-workspace
// override file substitutes for the built-in body.
func TestBuildRespectsTemplateOverride(t *testing.T) {
	t.Parallel()

	root := seedWorkspace(t, nil, "id: ws\nname: WS\n")
	override := "# Custom workspace primer\n\nThe team's preferred wording goes here.\n"
	if err := os.WriteFile(filepath.Join(root, ".rex", TemplateFilename),
		[]byte(override), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	out, err := Build(Options{
		WorkspaceRoot: root,
		FromTask:      "spec.task",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "Custom workspace primer") {
		t.Fatalf("override body missing:\n%s", out)
	}
	// Built-in headers must NOT appear when override is in use.
	if strings.Contains(out, "## Conventions") {
		t.Fatalf("override should suppress built-in body:\n%s", out)
	}
	// Run section must STILL append after the override so the
	// per-run context isn't lost.
	if !strings.Contains(out, "## Current run") {
		t.Fatalf("run section should append even with override:\n%s", out)
	}
}

// TestBuildEmptyWorkspaceRootReturnsEmpty covers the
// "skip-the-brief" signal for callers that opted out by
// passing an empty root.
func TestBuildEmptyWorkspaceRootReturnsEmpty(t *testing.T) {
	t.Parallel()
	out, err := Build(Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty brief: %q", out)
	}
}

// TestWrapPrependsBriefWithSeparator covers the prompt-wrap
// surface that runtask uses to inject the brief.
func TestWrapPrependsBriefWithSeparator(t *testing.T) {
	t.Parallel()

	got := Wrap("# brief\n\nbody", "do the thing")
	if !strings.HasPrefix(got, "# brief") {
		t.Fatalf("brief should lead: %q", got)
	}
	if !strings.Contains(got, "\n\n---\n\n") {
		t.Fatalf("expected separator: %q", got)
	}
	if !strings.Contains(got, "do the thing") {
		t.Fatalf("user prompt missing: %q", got)
	}
}

func TestWrapPassthroughOnEmptyBrief(t *testing.T) {
	t.Parallel()
	got := Wrap("", "do the thing")
	if got != "do the thing" {
		t.Fatalf("got %q want passthrough", got)
	}
}
