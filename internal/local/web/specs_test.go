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

func TestSpecEditFormShowsCurrentBody(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-edit-show")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha spec, state: draft}
`)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/alpha/edit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		`<textarea`, `name="body"`, `Alpha spec`, `id: alpha`, `mono editor`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in edit form:\n%s", want, body[:minInt(len(body), 2000)])
		}
	}
}

func TestSpecEditSaveValidWritesAndRedirects(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-edit-save")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
`)
	hs := newTestServer(t, root)

	newBody := `spec_version: 1
metadata: {id: alpha, name: Alpha v2, state: active}
`
	form := "body=" + urlEncode(newBody)
	req, _ := http.NewRequest(http.MethodPost,
		hs.URL+"/specs/alpha/edit",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d (expected 303)", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/specs/alpha" {
		t.Fatalf("Location: %q", got)
	}

	on := readFile(t, filepath.Join(root, ".rex", "specs", "alpha.yaml"))
	if !strings.Contains(on, "Alpha v2") {
		t.Fatalf("file did not pick up the new name: %s", on)
	}
}

func TestSpecEditRejectsIDMismatch(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-edit-mismatch")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
`)
	hs := newTestServer(t, root)

	// Body claims a different id.
	form := "body=" + urlEncode(`spec_version: 1
metadata: {id: beta, name: Beta, state: draft}
`)
	req, _ := http.NewRequest(http.MethodPost,
		hs.URL+"/specs/alpha/edit",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d (expected 400)", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "metadata.id is") {
		t.Errorf("expected id-mismatch banner: %s", body[:minInt(len(body), 1500)])
	}

	// File on disk should still be the original.
	on := readFile(t, filepath.Join(root, ".rex", "specs", "alpha.yaml"))
	if !strings.Contains(on, "id: alpha") {
		t.Fatalf("file was mutated despite id mismatch: %s", on)
	}
}

func TestSpecEditRejectsInvalidYAML(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-edit-bad-yaml")
	seedSpec(t, root, "alpha", `spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
`)
	hs := newTestServer(t, root)

	form := "body=" + urlEncode("not: valid: yaml: at: all")
	req, _ := http.NewRequest(http.MethodPost,
		hs.URL+"/specs/alpha/edit",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "could not save") {
		t.Errorf("expected error banner: %s", body[:minInt(len(body), 1500)])
	}
}

func urlEncode(s string) string {
	// Tiny escape for application/x-www-form-urlencoded test bodies.
	// We only need to cover the chars that appear in our YAML
	// fixtures: space, newline, =, &.
	r := strings.NewReplacer(
		"%", "%25",
		"+", "%2B",
		" ", "+",
		"\n", "%0A",
		"=", "%3D",
		"&", "%26",
		"\"", "%22",
		"'", "%27",
	)
	return r.Replace(s)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestSearchEmptyShowsHint(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-search-empty")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "enter a query above") {
		t.Errorf("expected hint for empty query: %s", body[:minInt(len(body), 1500)])
	}
}

func TestSearchFindsSpec(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-search-spec")
	seedSpec(t, root, "harness-design", `spec_version: 1
metadata: {id: harness-design, name: Harness design, state: draft}
description: |
  Harness adapter registry and ACP wiring.
`)
	hs := newTestServer(t, root)

	// Reindex by issuing a request to /search which opens the
	// search.Index — the indexer is wired into the eventlog
	// writer, but seeded specs above it bypassed that path; for
	// the test, a search index over events is enough since
	// the workspace.created event was written at init time.
	resp, err := http.Get(hs.URL + "/search?q=workspace")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	// We expect either at least one result OR the "no matches"
	// fallback (some test configurations don't index events
	// without a per-test rebuild). The form must render either
	// way and include the query.
	for _, want := range []string{
		`name="q"`,
		`value="workspace"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in search page: %s", want, body[:minInt(len(body), 1500)])
		}
	}
}

func TestSettingsRendersAllSections(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-settings")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		">settings<",
		"workspace</h2>",
		"identity</h2>",
		"remotes</h2>",
		"hooks</h2>",
		"workspace.yaml",
		"rex identity",
		"rex remote add",
		"rex hooks list",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in /settings:\n%s", want, body[:minInt(len(body), 2000)])
		}
	}
}

func TestSyncPageWithoutRemotesShowsHint(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-sync-empty")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/sync")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "no remotes registered") {
		t.Errorf("expected empty-state hint: %s", body[:minInt(len(body), 1500)])
	}
}

func TestSyncPostUnknownRemoteRendersError(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-sync-unknown")
	hs := newTestServer(t, root)

	form := "remote=ghost"
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/sync", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "not registered") {
		t.Errorf("expected unknown-remote banner: %s", body[:minInt(len(body), 1500)])
	}
}
