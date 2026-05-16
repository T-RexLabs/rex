package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// remotesTOML returns a minimal TOML body the central remotes
// projection can parse. Tests use it to populate the stub
// GitStore at the workspace's `.rex/remotes.toml` path
// (storage.WS.2.7).
func remotesTOML(entries ...struct{ name, url, fingerprint string }) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("[")
		b.WriteString(e.name)
		b.WriteString("]\nurl = \"")
		b.WriteString(e.url)
		b.WriteString("\"\nadded_at = 2026-01-01T00:00:00Z\n")
		if e.fingerprint != "" {
			b.WriteString("fingerprint = \"")
			b.WriteString(e.fingerprint)
			b.WriteString("\"\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TestCentralRemotesRendersFromGitStore exercises the happy path:
// two remotes from the workspace's synced remotes.toml surface on
// the page, sorted by name, with the central-specific source
// label, and without the local-only "add via …" affordance.
func TestCentralRemotesRendersFromGitStore(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"remotes.toml": remotesTOML(
			struct{ name, url, fingerprint string }{"primary", "https://central.example", "abc123def456"},
			struct{ name, url, fingerprint string }{"backup", "https://central.backup.example", ""},
		),
	})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/remotes")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "primary") || !strings.Contains(html, "https://central.example") {
		t.Errorf("primary remote missing: %s", html)
	}
	if !strings.Contains(html, "backup") || !strings.Contains(html, "https://central.backup.example") {
		t.Errorf("backup remote missing: %s", html)
	}
	// Source label — the apostrophe in "workspace's" is
	// HTML-escaped by html/template, so look for the
	// surrounding unambiguous fragment.
	if !strings.Contains(html, "synced .rex/remotes.toml") {
		t.Errorf("central source label missing: %s", html)
	}
	if strings.Contains(html, "add via") {
		t.Errorf("local-only 'add via' affordance leaked into central remotes page: %s", html)
	}
	// Sort order — backup should appear before primary
	// alphabetically. Anchor the search to the per-row <code>
	// cell so unrelated occurrences (CSS class names, JS
	// asset names) don't skew the comparison.
	if i, j := strings.Index(html, "<code>backup</code>"), strings.Index(html, "<code>primary</code>"); i < 0 || j < 0 || i >= j {
		t.Errorf("remotes not sorted by name: backup@%d primary@%d", i, j)
	}
}

// TestCentralRemotesEmptyWhenWorkspaceHasNone confirms a
// workspace without a synced remotes.toml renders cleanly (no
// 500) with the empty-state message.
func TestCentralRemotesEmptyWhenWorkspaceHasNone(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{}) // empty GitStore
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/remotes")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no remotes registered for this workspace") {
		t.Errorf("missing empty-state copy: %s", body)
	}
}

// workspaceYAML returns a minimal workspace.yaml body for the
// central settings page tests.
func workspaceYAML(id, name, state string) string {
	return `id: ` + id + `
name: ` + name + `
state: ` + state + `
created_at: 2026-01-01T00:00:00Z
`
}

// TestCentralSettingsRendersWorkspaceYAML covers the happy path:
// workspace.yaml from the GitStore renders both as a structured
// field list and as the raw YAML block.
func TestCentralSettingsRendersWorkspaceYAML(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"workspace.yaml": workspaceYAML("ws-acme", "Acme Workspace", "active"),
	})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-acme/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "Acme Workspace") {
		t.Errorf("name not rendered: %s", html)
	}
	if !strings.Contains(html, "ws-acme") {
		t.Errorf("id not rendered: %s", html)
	}
	if !strings.Contains(html, "active") {
		t.Errorf("state not rendered: %s", html)
	}
	// Per-machine config sections (identity / hooks / log levels)
	// must not appear on central. Anchor to the local section
	// header text so unrelated occurrences (CSS, JS asset
	// names) don't trip the assertion.
	if strings.Contains(html, "<h2>identity</h2>") || strings.Contains(html, "rex identity</code>") {
		t.Errorf("identity section leaked into central settings: %s", html)
	}
	if strings.Contains(html, "<h2>hooks</h2>") {
		t.Errorf("hooks section leaked into central settings: %s", html)
	}
}

// TestCentralSettingsEmptyWorkspaceYAML covers the no-synced-yaml
// branch — page renders with the empty-state messages rather
// than 500.
func TestCentralSettingsEmptyWorkspaceYAML(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no workspace.yaml synced") {
		t.Errorf("missing empty-state copy: %s", body)
	}
}

// TestCentralRemotesAndSettings503WithoutResolver covers the
// misconfigured-deployment branch for the two new routes.
func TestCentralRemotesAndSettings503WithoutResolver(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, path := range []string{
		"/orgs/acme/workspaces/ws-1/remotes",
		"/orgs/acme/workspaces/ws-1/settings",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d (want 503)", path, resp.StatusCode)
		}
	}
}
