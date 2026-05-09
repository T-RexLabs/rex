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

// TestSpecRunsTabRendersAskForm covers the form's presence on
// the runs tab when at least one harness adapter is registered.
func TestSpecRunsTabRendersAskForm(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-form")
	seedSpecForAskForm(t, root)

	reg := adapter.NewRegistry()
	if err := reg.Register(testStaticAdapter{name: "opencode"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: reg})

	resp, err := http.Get(hs.URL + "/specs/target?tab=runs")
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

// TestSpecRunsTabHidesFormWithoutHarnesses covers the
// no-harness fallback hint.
func TestSpecRunsTabHidesFormWithoutHarnesses(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-ask-noharness")
	seedSpecForAskForm(t, root)
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: adapter.NewRegistry()})

	resp, err := http.Get(hs.URL + "/specs/target?tab=runs")
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
