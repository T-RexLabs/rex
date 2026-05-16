package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// amendmentYAML returns a minimal but well-formed amendment file
// targeting the given spec id. Used to populate the stub
// GitStore so the central amendments projection's parse path is
// exercised end-to-end.
func amendmentYAML(target, date, state, summary string) string {
	return `# test amendment
amendment_for: ` + target + `
amendment_date: ` + date + `
state: ` + state + `
summary: |
  ` + summary + `
amendment_kind: additive
`
}

func newAmendmentsServer(t *testing.T, entries map[string]string) *httptest.Server {
	t.Helper()
	store := stubGitStore{entries: entries}
	s, err := New(Options{
		Version:  "test",
		Resolver: NewGitStoreResolver(store, nil),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestCentralAmendmentsListProposedAndAccepted exercises the
// happy path: proposed + accepted entries from the GitStore both
// surface in the list, sorted by stem.
func TestCentralAmendmentsListProposedAndAccepted(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"specs/_proposed/audit-amendment-2026-01-01.yaml":            amendmentYAML("audit", "2026-01-01", "proposed", "Tweak audit catalog."),
		"specs/_proposed/_accepted/web-ui-amendment-2026-01-02.yaml": amendmentYAML("web-ui", "2026-01-02", "accepted", "Split central routes."),
		// Not-an-amendment GitStore entries — must be ignored.
		"specs/sync.yaml": "fake-spec",
		"workspace.yaml":  "fake-ws",
		"specs/_proposed/_accepted/nested/extra.yaml": "should-be-ignored",
	})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/amendments")
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
	if !strings.Contains(html, "audit-amendment-2026-01-01") {
		t.Errorf("proposed amendment missing: %s", html)
	}
	if !strings.Contains(html, "web-ui-amendment-2026-01-02") {
		t.Errorf("accepted amendment missing: %s", html)
	}
	if strings.Contains(html, "fake-spec") || strings.Contains(html, "fake-ws") {
		t.Errorf("non-amendment entry leaked into amendments list: %s", html)
	}
	if strings.Contains(html, "should-be-ignored") {
		t.Errorf("nested file leaked into amendments list: %s", html)
	}
}

// TestCentralAmendmentsListRespectsStateFilter checks the
// ?state=proposed path narrows to proposed-only.
func TestCentralAmendmentsListRespectsStateFilter(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"specs/_proposed/audit-amendment-2026-01-01.yaml":         amendmentYAML("audit", "2026-01-01", "proposed", "Proposed one."),
		"specs/_proposed/_accepted/cli-amendment-2026-01-02.yaml": amendmentYAML("cli", "2026-01-02", "accepted", "Accepted one."),
	})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/amendments?state=proposed")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	if !strings.Contains(html, "audit-amendment-2026-01-01") {
		t.Errorf("proposed filter dropped the proposed entry: %s", html)
	}
	if strings.Contains(html, "cli-amendment-2026-01-02") {
		t.Errorf("proposed filter leaked an accepted entry: %s", html)
	}
}

// TestCentralAmendmentsListRespectsForFilter narrows by target
// spec id (the ?for=<spec-id> query).
func TestCentralAmendmentsListRespectsForFilter(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"specs/_proposed/audit-amendment-2026-01-01.yaml": amendmentYAML("audit", "2026-01-01", "proposed", "For audit."),
		"specs/_proposed/cli-amendment-2026-01-02.yaml":   amendmentYAML("cli", "2026-01-02", "proposed", "For cli."),
	})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/amendments?for=audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	if !strings.Contains(html, "audit-amendment-2026-01-01") {
		t.Errorf("for=audit dropped the audit entry: %s", html)
	}
	if strings.Contains(html, "cli-amendment-2026-01-02") {
		t.Errorf("for=audit leaked a cli entry: %s", html)
	}
}

// TestCentralAmendmentDetailRendersFromProposed exercises the
// detail page for a proposed amendment.
func TestCentralAmendmentDetailRendersFromProposed(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"specs/_proposed/audit-amendment-2026-01-01.yaml": amendmentYAML("audit", "2026-01-01", "proposed", "Tweak catalog."),
	})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/amendments/audit-amendment-2026-01-01")
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
	if !strings.Contains(html, "Tweak catalog") {
		t.Errorf("summary not rendered: %s", html)
	}
	// Read-only on central: the template's accept/reject buttons
	// must not appear because state has been munged away from
	// the literal "proposed".
	if strings.Contains(html, "/amendments/audit-amendment-2026-01-01/accept") {
		t.Errorf("accept form leaked into central detail page: %s", html)
	}
}

// TestCentralAmendmentDetail404 covers the missing-stem branch.
func TestCentralAmendmentDetail404(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/amendments/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}

// TestCentralAmendmentStateForPath documents the path-shape rules
// so the projection's filter doesn't regress silently.
func TestCentralAmendmentStateForPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		wantOK bool
		state  string
	}{
		{"specs/_proposed/foo.yaml", true, "proposed"},
		{"specs/_proposed/_accepted/foo.yaml", true, "accepted"},
		{"specs/_proposed/sub/foo.yaml", false, ""},
		{"specs/_proposed/_accepted/sub/foo.yaml", false, ""},
		{"specs/foo.yaml", false, ""},
		{"workspace.yaml", false, ""},
		{"specs/_proposed/foo.txt", false, ""},
	}
	for _, tc := range cases {
		state, ok := amendmentStateForPath(tc.in)
		if ok != tc.wantOK {
			t.Errorf("amendmentStateForPath(%q) ok=%t want=%t", tc.in, ok, tc.wantOK)
		}
		if ok && string(state) != tc.state {
			t.Errorf("amendmentStateForPath(%q) state=%q want=%q", tc.in, state, tc.state)
		}
	}
}

// TestCentralSearchRendersNoticeWithoutProjection covers the v1
// path: central's resolver leaves Search nil, the handler still
// renders the page but with the "backend not yet wired" notice.
func TestCentralSearchRendersNoticeWithoutProjection(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{}) // no events store either

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/search?q=hello")
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
	if !strings.Contains(html, "central search backend not yet wired") {
		t.Errorf("missing v1 notice on central search: %s", html)
	}
	// The query echoes back so the URL is shareable.
	if !strings.Contains(html, `value="hello"`) {
		t.Errorf("query value not preserved on the form: %s", html)
	}
}

// TestCentralSearchWithoutResolverReturns503 covers the
// misconfigured-deployment branch for /search.
func TestCentralSearchWithoutResolverReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (want 503)", resp.StatusCode)
	}
}
