package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalweb "github.com/asabla/rex/internal/web"
)

// noFollowHTTPClient returns an http.Client that refuses to
// follow 3xx redirects so the test can assert on the Location
// header directly.
func noFollowHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// TestHomeRedirectsToSingleOrg covers the common case: a user
// with one membership lands at GET / and is 303'd straight to
// that org. The make web-dev path (dev identity, single
// default org) trips this branch.
func TestHomeRedirectsToSingleOrg(t *testing.T) {
	t.Parallel()
	orgs := &stubOrgs{
		orgs: map[string]internalweb.OrgSummary{
			"acme": {ID: "acme", Name: "acme", DisplayName: "Acme Co"},
		},
		roles: map[string]map[string]string{
			"acme": {"fp-alice": "admin"},
		},
	}
	s, err := New(Options{
		Version: "test",
		Auth:    &stubAuth{validTokens: map[string]SessionInfo{"tok-alice": {Fingerprint: "fp-alice"}}},
		Orgs:    orgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := noFollowHTTPClient()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-alice"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/orgs/acme" {
		t.Errorf("Location: %q want /orgs/acme", got)
	}
}

// TestHomeRendersPickerForMultiOrg covers the multi-org branch:
// the user belongs to two orgs, the home page renders a list
// with both linked.
func TestHomeRendersPickerForMultiOrg(t *testing.T) {
	t.Parallel()
	orgs := &stubOrgs{
		orgs: map[string]internalweb.OrgSummary{
			"acme":  {ID: "acme", Name: "acme", DisplayName: "Acme Co"},
			"beta":  {ID: "beta", Name: "beta", DisplayName: "Beta Inc"},
			"third": {ID: "third", Name: "third", DisplayName: "Third Co"},
		},
		roles: map[string]map[string]string{
			"acme": {"fp-alice": "admin"},
			"beta": {"fp-alice": "member"},
		},
	}
	s, _ := New(Options{
		Version: "test",
		Auth:    &stubAuth{validTokens: map[string]SessionInfo{"tok-alice": {Fingerprint: "fp-alice"}}},
		Orgs:    orgs,
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-alice"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, `href="/orgs/acme"`) || !strings.Contains(html, `href="/orgs/beta"`) {
		t.Errorf("multi-org picker missing one of the org links: %s", html)
	}
	if strings.Contains(html, `href="/orgs/third"`) {
		t.Errorf("picker leaked an org alice isn't a member of: %s", html)
	}
}

// TestHomeWithoutOrgsProjectionFallsBackToDefault covers the
// dev-without-pg path: Orgs is nil, the handler 303s to
// /orgs/default rather than 404'ing or hitting a nil receiver.
func TestHomeWithoutOrgsProjectionFallsBackToDefault(t *testing.T) {
	t.Parallel()
	s, _ := New(Options{Version: "test"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := noFollowHTTPClient()
	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/orgs/default" {
		t.Errorf("Location: %q want /orgs/default", got)
	}
}

// TestHomeNoMembershipsRendersFriendlyNotice covers the
// signed-in-but-unaffiliated case: a fingerprint that no org
// has accepted yet. The page should explain the next step
// rather than 404 or empty-render.
func TestHomeNoMembershipsRendersFriendlyNotice(t *testing.T) {
	t.Parallel()
	orgs := &stubOrgs{
		orgs: map[string]internalweb.OrgSummary{
			"acme": {ID: "acme", Name: "acme"},
		},
		// no roles for fp-bob anywhere
	}
	s, _ := New(Options{
		Version: "test",
		Auth:    &stubAuth{validTokens: map[string]SessionInfo{"tok-bob": {Fingerprint: "fp-bob"}}},
		Orgs:    orgs,
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-bob"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "rex remote join") {
		t.Errorf("page should hint at the next step: %s", body)
	}
}
