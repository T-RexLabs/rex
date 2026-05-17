package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
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

// TestWorkspaceOverviewRendersStatsAndRecentRuns covers the
// dashboard parity bits added on top of the card grid: the
// header meta row carries spec + run counts, and a recent-runs
// section renders a table of the most recent runs synced into
// the workspace (capped at 5). Prevents a regression to the
// "sitemap of links" shape the page started out with.
func TestWorkspaceOverviewRendersStatsAndRecentRuns(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	store := stubEventStore{records: []eventlog.Record{
		runEventRecord(t, "ev-1", t1, runner.RunStartedEvent{RunID: "run-aaa", StartedAt: t1}),
		runEventRecord(t, "ev-2", t1.Add(time.Second), runner.RunCompletedEvent{RunID: "run-aaa", CompletedAt: t1.Add(time.Second)}),
		runEventRecord(t, "ev-3", t2, runner.RunStartedEvent{RunID: "run-bbb", StartedAt: t2}),
	}}
	git := stubGitStore{entries: map[string]string{
		"workspace.yaml":  workspaceYAML("ws-1", "Acme Workspace", "active"),
		"specs/alpha.yaml": validSpecYAML("alpha", "Alpha"),
		"specs/beta.yaml":  validSpecYAML("beta", "Beta"),
	}}
	s, err := New(Options{
		Version:  "test",
		Resolver: NewGitStoreResolver(git, store),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

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

	// Stat row.
	if !strings.Contains(html, "2 specs") {
		t.Errorf("spec count missing: %s", html)
	}
	if !strings.Contains(html, "2 runs") {
		t.Errorf("run count missing: %s", html)
	}

	// Recent runs section is rendered with click-throughs scoped
	// to the org + workspace.
	if !strings.Contains(html, "recent runs") {
		t.Errorf("recent runs section missing: %s", html)
	}
	if !strings.Contains(html, `href="/orgs/acme/workspaces/ws-1/runs/run-aaa"`) {
		t.Errorf("run-aaa link missing or wrong link base: %s", html)
	}
	if !strings.Contains(html, `href="/orgs/acme/workspaces/ws-1/runs/run-bbb"`) {
		t.Errorf("run-bbb link missing: %s", html)
	}
	// The "all runs" footer link points at the workspace-scoped
	// runs list, not /runs.
	if !strings.Contains(html, `href="/orgs/acme/workspaces/ws-1/runs"`) {
		t.Errorf("all-runs section link missing: %s", html)
	}
}

// TestWorkspaceOverviewStatsZeroWhenEmpty covers the
// best-effort branch: an empty workspace (no specs synced, no
// events) renders the stat row with zeros + the empty recent-
// runs copy instead of 500.
func TestWorkspaceOverviewStatsZeroWhenEmpty(t *testing.T) {
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
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "0 specs") {
		t.Errorf("zero-spec stat missing: %s", html)
	}
	if !strings.Contains(html, "0 runs") {
		t.Errorf("zero-run stat missing: %s", html)
	}
	if !strings.Contains(html, "no runs synced yet") {
		t.Errorf("empty-runs copy missing: %s", html)
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
