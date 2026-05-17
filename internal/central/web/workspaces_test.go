package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCentralWorkspacesIndexRendersGitStoreWorkspace covers the
// v1 single-workspace shape: the page lists the one workspace
// bound to the central GitStore with a click-through to
// /orgs/<org>/workspaces/<ws-id>/specs.
func TestCentralWorkspacesIndexRendersGitStoreWorkspace(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"workspace.yaml": workspaceYAML("ws-1", "Acme Workspace", "active"),
	})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces")
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
	if !strings.Contains(html, "ws-1") {
		t.Errorf("workspace id missing: %s", html)
	}
	if !strings.Contains(html, "Acme Workspace") {
		t.Errorf("workspace name missing: %s", html)
	}
	if !strings.Contains(html, `href="/orgs/acme/workspaces/ws-1"`) {
		t.Errorf("click-through link missing or wrong: %s", html)
	}
	// Org id appears in the header meta.
	if !strings.Contains(html, "<code>acme</code>") {
		t.Errorf("org id not surfaced in header: %s", html)
	}
}

// TestWorkspaceOverviewRendersCardGrid covers the new per-
// workspace landing page: header shows the workspace name +
// id + state pill, and the card grid links to each
// workspace-scoped sub-surface.
func TestWorkspaceOverviewRendersCardGrid(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"workspace.yaml": workspaceYAML("ws-1", "Acme Workspace", "active"),
	})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1")
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
		t.Errorf("workspace name missing from header: %s", html)
	}
	for _, link := range []string{
		`href="/orgs/acme/workspaces/ws-1/specs"`,
		`href="/orgs/acme/workspaces/ws-1/runs"`,
		`href="/orgs/acme/workspaces/ws-1/audit"`,
		`href="/orgs/acme/workspaces/ws-1/amendments"`,
		`href="/orgs/acme/workspaces/ws-1/search"`,
		`href="/orgs/acme/workspaces/ws-1/remotes"`,
		`href="/orgs/acme/workspaces/ws-1/settings"`,
	} {
		if !strings.Contains(html, link) {
			t.Errorf("card link missing: %s", link)
		}
	}
	if !strings.Contains(html, "card-grid") {
		t.Errorf("dashboard card-grid not rendered: %s", html)
	}
	if !strings.Contains(html, `pill-active`) {
		t.Errorf("state pill missing: %s", html)
	}
}

// TestWorkspaceOverviewIdOnlyWhenYAMLMissing covers the
// best-effort branch: a workspace whose workspace.yaml hasn't
// synced yet still renders, with the id as the heading and
// no state pill.
func TestWorkspaceOverviewIdOnlyWhenYAMLMissing(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		// no workspace.yaml; the resolver still resolves ws-1
	})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d (want 200 even without yaml)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "ws-1") {
		t.Errorf("id-only heading missing: %s", html)
	}
	if strings.Contains(html, "pill-active") {
		t.Errorf("state pill rendered without yaml metadata: %s", html)
	}
}

// TestCentralWorkspacesIndexEmptyState confirms a fresh
// deployment with no workspace.yaml synced renders the empty
// state rather than 500.
func TestCentralWorkspacesIndexEmptyState(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no workspaces yet") {
		t.Errorf("missing empty-state copy: %s", body)
	}
}

// TestCentralWorkspacesIndex503WithoutResolver covers the
// misconfigured-deployment branch.
func TestCentralWorkspacesIndex503WithoutResolver(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (want 503)", resp.StatusCode)
	}
}
