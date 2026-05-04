package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedSpec(t *testing.T, root, id, body string) {
	t.Helper()
	path := filepath.Join(root, ".rex", "specs", id+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed spec %s: %v", id, err)
	}
}

func TestSpecsListEmpty(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-empty-specs")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "no specs yet") {
		t.Fatalf("expected empty hint: %s", body)
	}
}

func TestSpecsListPopulated(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-list")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha spec, state: draft}
components:
  AUTH:
    name: Auth
    requirements:
      "1": exists
tasks:
  - id: t1
    description: do
    state: todo
`)
	seedSpec(t, root, "beta", `spec_version: 1
metadata: {id: beta, name: Beta spec, state: active}
`)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"alpha", "beta", "Alpha spec", "Beta spec", "draft", "active"} {
		if !strings.Contains(body, want) {
			t.Errorf("/specs missing %q\n%s", want, body[:minInt(len(body), 1500)])
		}
	}
}

func TestSpecDetailRenderedTab(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-detail")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha spec, state: draft}
description: |
  Alpha covers the auth surface.
components:
  AUTH:
    name: Auth flow
    requirements:
      "1": Validate every request
`)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/alpha")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	for _, want := range []string{
		"Alpha spec",
		"alpha covers the auth surface",
		"AUTH",
		"alpha.AUTH.1",
		`aria-selected="true"`,
		`href="?tab=source"`,
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("missing %q\n%s", want, body[:minInt(len(body), 2500)])
		}
	}
}

func TestSpecDetailSourceTab(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-source")
	rawBody := `spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
`
	seedSpec(t, root, "alpha", rawBody)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/alpha?tab=source")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	// Chroma tokenizes the YAML so the raw substring no longer
	// appears verbatim — assert on the tokens that survive
	// highlighting plus the wrapper class so we still know the
	// page is the source tab and not, say, the rendered tab.
	if !strings.Contains(body, "spec_version") {
		t.Fatalf("source tab missing spec_version token: %s", body)
	}
	if !strings.Contains(body, `class="source chroma"`) {
		t.Fatalf("source tab chroma wrapper missing: %s", body)
	}
	if !strings.Contains(body, `class="language-yaml"`) {
		t.Fatalf("source tab language class missing: %s", body)
	}
}

func TestSpecDetailTasksTab(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-tasks")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
tasks:
  - id: design-api
    description: Sketch the surface
    state: todo
    references: [AUTH.1, BILLING.2]
`)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/alpha?tab=tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"design-api", "Sketch the surface", "AUTH.1", "BILLING.2"} {
		if !strings.Contains(body, want) {
			t.Errorf("tasks tab missing %q\n%s", want, body[:minInt(len(body), 2000)])
		}
	}
}

func TestSpecDetailDefaultsToRendered(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-default-tab")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
description: |
  rendered by default
`)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/alpha")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "rendered by default") {
		t.Fatalf("expected description in default tab: %s", body)
	}
}

func TestSpecDetailUnknownIDReturns404(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-404-spec")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/ghost")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestSpecDetailRejectsNonKebabID(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-bad-id")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/Bad%20ID")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
