package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newGatedServer constructs a central web shell with Auth wired
// so the session gate fires. The supplied valid tokens land in
// the stub's validTokens map; everything else is rejected.
func newGatedServer(t *testing.T, validTokens ...string) *httptest.Server {
	t.Helper()
	allowed := make(map[string]SessionInfo, len(validTokens))
	for _, tok := range validTokens {
		allowed[tok] = SessionInfo{Fingerprint: "fp-test", ExpiresAt: time.Now().Add(time.Minute)}
	}
	s, err := New(Options{
		Version:  "test",
		Auth:     &stubAuth{validTokens: allowed},
		Resolver: NewGitStoreResolver(stubGitStore{entries: map[string]string{}}, nil),
		Orgs:     &stubOrgs{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// noFollowClient returns an http.Client that surfaces 3xx
// responses to the caller instead of automatically following
// them. The gate-redirect assertions need to inspect the 303
// directly rather than chase it to /login.
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// TestSessionGateRedirectsUnauthenticatedGetToLogin covers the
// browser-flow: an unauthed GET on a gated route bounces to
// /login with the original target preserved in ?redirect=.
func TestSessionGateRedirectsUnauthenticatedGetToLogin(t *testing.T) {
	t.Parallel()
	srv := newGatedServer(t /* no valid tokens */)
	c := noFollowClient()

	resp, err := c.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs?tab=source")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d (want 303) body: %s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/login?redirect=") {
		t.Errorf("Location: %q (want /login?redirect=…)", loc)
	}
	// The original path + query must round-trip through the
	// redirect so /auth/redeem can land the user back on it.
	if !strings.Contains(loc, "%2Forgs%2Facme%2Fworkspaces%2Fws-1%2Fspecs") {
		t.Errorf("Location missing original path: %q", loc)
	}
	if !strings.Contains(loc, "tab%3Dsource") {
		t.Errorf("Location dropped original query: %q", loc)
	}
}

// TestSessionGateAcceptsValidCookie covers the happy path: a
// browser arriving with rex_session matching a known token gets
// the page rendered (or its underlying 503 for nil resolvers,
// but here the resolver is wired so the response status is 200).
func TestSessionGateAcceptsValidCookie(t *testing.T) {
	t.Parallel()
	srv := newGatedServer(t, "tok-valid")
	c := noFollowClient()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/orgs/acme/workspaces/ws-1/specs", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-valid"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d (want 200)", resp.StatusCode)
	}
}

// TestSessionGateAcceptsValidBearer mirrors the cookie test for
// the API-consumer path (Authorization: Bearer).
func TestSessionGateAcceptsValidBearer(t *testing.T) {
	t.Parallel()
	srv := newGatedServer(t, "tok-valid")
	c := noFollowClient()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/orgs/acme/workspaces/ws-1/specs", nil)
	req.Header.Set("Authorization", "Bearer tok-valid")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d (want 200)", resp.StatusCode)
	}
}

// TestSessionGateRejectsInvalidCookie keeps the boundary tight:
// a present-but-unknown rex_session is treated identically to
// no cookie at all.
func TestSessionGateRejectsInvalidCookie(t *testing.T) {
	t.Parallel()
	srv := newGatedServer(t, "tok-good")
	c := noFollowClient()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/orgs/acme/workspaces/ws-1/specs", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-stolen-or-stale"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d (want 303)", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Location"), "/login?redirect=") {
		t.Errorf("Location: %q", resp.Header.Get("Location"))
	}
}

// TestSessionGatePublicRoutesBypass confirms /login, /_web/health,
// and /static/* are reachable without a session. The gate would
// loop unauthenticated browsers forever if /login itself were
// gated.
func TestSessionGatePublicRoutesBypass(t *testing.T) {
	t.Parallel()
	srv := newGatedServer(t /* no valid tokens */)
	for _, path := range []string{
		"/_web/health",
		"/login",
		"/static/app.css",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d (want 200)", path, resp.StatusCode)
		}
	}
}

// TestSessionGate401sUnauthenticatedMutations covers the
// non-GET branch: redirecting a POST/PUT would silently drop the
// request body, so the gate 401s instead. v1 has no mutating web
// routes, but the boundary is still asserted so future
// admin-mutation pages inherit the behaviour.
func TestSessionGate401sUnauthenticatedMutations(t *testing.T) {
	t.Parallel()
	srv := newGatedServer(t /* no valid tokens */)
	c := noFollowClient()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/orgs/acme/workspaces/ws-1/specs", strings.NewReader(""))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	// 401 for unauthed mutation; central web has no POST routes
	// yet so the gate fires before the mux's method-mismatch
	// branch.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d (want 401)", resp.StatusCode)
	}
}

// TestSessionGatePassThroughWhenAuthNil confirms the dev-mode
// path: a Server constructed without an Auth keeps every route
// open. Production deployments always set --keys + --db so Auth
// is wired; this test pins the convention that omitting it
// disables the gate cleanly instead of locking everything down.
func TestSessionGatePassThroughWhenAuthNil(t *testing.T) {
	t.Parallel()
	s, err := New(Options{
		Version:  "test",
		Resolver: NewGitStoreResolver(stubGitStore{entries: map[string]string{}}, nil),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	// 200 with empty page content — no redirect, no 401.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d (want 200 — gate should be off without Auth)", resp.StatusCode)
	}
}
