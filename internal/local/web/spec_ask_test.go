package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

// seedSpecForAskForm drops a spec into the test workspace so
// the ad-hoc-action handler has something to load.
func seedSpecForAskForm(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `spec_version: 1
metadata:
  id: target
  name: Target
  state: draft
`
	if err := os.WriteFile(filepath.Join(dir, "target.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

// TestSpecAskTabRendersAskForm covers the form's presence on
// the ask tab when at least one harness adapter is registered.
// (The ask form moved off the runs tab to its own tab — see the
// 2026-05-09 spec-detail polish round.)
func TestSpecAskTabRendersAskForm(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-form")
	seedSpecForAskForm(t, root)

	reg := adapter.NewRegistry()
	if err := reg.Register(testStaticAdapter{name: "opencode"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: reg})

	resp, err := http.Get(hs.URL + "/specs/target?tab=ask")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		`<form method="post" action="/specs/target/ask"`,
		`name="action"`,
		`value="review"`,
		`value="amend"`,
		`value="draft"`,
		`name="harness"`,
		`>opencode<`,
		`name="prompt"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body[:minInt(len(body), 4500)])
		}
	}
}

// TestSpecAskTabHidesFormWithoutHarnesses covers the
// no-harness fallback hint.
func TestSpecAskTabHidesFormWithoutHarnesses(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-noharness")
	seedSpecForAskForm(t, root)
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: adapter.NewRegistry()})

	resp, err := http.Get(hs.URL + "/specs/target?tab=ask")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if strings.Contains(body, `<form method="post" action="/specs/target/ask"`) {
		t.Fatalf("form should not render without harnesses:\n%s", body[:minInt(len(body), 2500)])
	}
	if !strings.Contains(body, "No harness adapters are registered") {
		t.Fatalf("expected fallback hint:\n%s", body[:minInt(len(body), 2500)])
	}
}

// TestSpecAskTabAppearsBetweenRenderedAndSource confirms the
// new tab's position in the nav so it lands where the user
// expects it (right after rendered).
func TestSpecAskTabAppearsBetweenRenderedAndSource(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-tab-order")
	seedSpecForAskForm(t, root)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/target")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	rendered := strings.Index(body, `href="?tab=rendered"`)
	ask := strings.Index(body, `href="?tab=ask"`)
	source := strings.Index(body, `href="?tab=source"`)
	if rendered < 0 || ask < 0 || source < 0 {
		t.Fatalf("missing one of rendered/ask/source tabs (idx: %d/%d/%d)", rendered, ask, source)
	}
	if rendered >= ask || ask >= source {
		t.Fatalf("tab order should be rendered → ask → source; got idx %d/%d/%d", rendered, ask, source)
	}
}

// TestSpecTaskRowHasStartARunLink covers the "start a run →"
// link added to every task row (including tasks without a
// run: recipe) so authors can attach a task to a fresh run
// without the recipe-only restriction.
func TestSpecTaskRowHasStartARunLink(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-start-run-link")
	dir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `spec_version: 1
metadata:
  id: target
  name: Target
  state: draft
tasks:
  - id: only-task
    description: TODO
    state: todo
`
	if err := os.WriteFile(filepath.Join(dir, "target.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/target?tab=tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	gotBody := readBody(t, resp)
	if !strings.Contains(gotBody, `href="/runs/new?from_task=target.only-task"`) {
		t.Fatalf("expected start-a-run link with prefilled from_task:\n%s", gotBody[:minInt(len(gotBody), 4000)])
	}
	if !strings.Contains(gotBody, `start a run →`) {
		t.Fatalf("expected start-a-run button label:\n%s", gotBody[:minInt(len(gotBody), 4000)])
	}
}

// TestSpecAskRejectsMissingHarness covers the validation
// surface: missing harness yields 400 + an error message.
func TestSpecAskRejectsMissingHarness(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-missing")
	seedSpecForAskForm(t, root)
	hs := newTestServer(t, root)

	form := strings.NewReader("action=review&harness=&prompt=hello")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/specs/target/ask", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "harness is required") {
		t.Fatalf("expected harness-required: %s", body)
	}
}

// TestSpecAskRejectsMissingPrompt covers the prompt-required check.
func TestSpecAskRejectsMissingPrompt(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-noprompt")
	seedSpecForAskForm(t, root)
	hs := newTestServer(t, root)

	form := strings.NewReader("action=review&harness=opencode&prompt=")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/specs/target/ask", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "prompt is required") &&
		!strings.Contains(body, "unknown harness") {
		t.Fatalf("expected validation error: %s", body)
	}
}

// TestSpecRunsTabHasNoViewRunsButton confirms the redundant
// header button is gone — the runs tab is the canonical
// surface now.
func TestSpecRunsTabHasNoViewRunsButton(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-no-view-runs")
	seedSpecForAskForm(t, root)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/target")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if strings.Contains(body, `>view runs</a>`) {
		t.Fatalf("expected view-runs button to be removed; the runs tab is the canonical surface:\n%s", body[:minInt(len(body), 2000)])
	}
	// The runs tab itself must still render.
	if !strings.Contains(body, `href="?tab=runs"`) {
		t.Fatalf("runs tab missing:\n%s", body[:minInt(len(body), 2000)])
	}
}
