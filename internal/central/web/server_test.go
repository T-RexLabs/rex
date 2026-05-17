package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// stubAuth is a deterministic Auth used in /login tests + the
// session-gate tests so coverage doesn't depend on the real
// central server's auth state. validTokens lists every token
// ValidateSession should accept; any other token returns
// errStubInvalidToken.
type stubAuth struct {
	pkg         proto.LoginChallengePackage
	err         error
	validTokens map[string]SessionInfo
}

var errStubInvalidToken = errors.New("stub: invalid token")

func (a *stubAuth) IssueLoginChallenge(hostname string) (proto.LoginChallengePackage, error) {
	if a.err != nil {
		return proto.LoginChallengePackage{}, a.err
	}
	p := a.pkg
	p.Hostname = hostname
	return p, nil
}

func (a *stubAuth) ValidateSession(token string) (SessionInfo, error) {
	if info, ok := a.validTokens[token]; ok {
		return info, nil
	}
	return SessionInfo{}, errStubInvalidToken
}

// TestNewParsesSharedRenderer confirms the central shell's
// constructor reaches the shared internal/web package and parses
// every page (web-ui.CENTRAL-LAYOUT.1). If this fails the package
// boundary is broken — internal/web's embed.FS isn't visible from
// internal/central/web, the FuncMap is missing a func a page
// references, or a template body changed in a way that won't
// compile in central's binary.
func TestNewParsesSharedRenderer(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Spot-check three pages from different roles. If the renderer
	// parsed at all every page is loaded; the explicit names guard
	// against a silent rename / removal of any of these in
	// internal/web/templates/pages.
	for _, page := range []string{"home.tmpl", "specs_list.tmpl", "audit.tmpl"} {
		if !s.HasPage(page) {
			t.Errorf("renderer missing page %q", page)
		}
	}
}

// TestHealthPingProvesWiring exercises GET /_web/health end-to-end.
// Verifies three things in one request: the mux dispatches the
// path, the page renders, and /static/ is wired (the page links to
// /static/app.css, which the static handler must be able to serve).
func TestHealthPingProvesWiring(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "1.2.3"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/_web/health")
	if err != nil {
		t.Fatalf("GET /_web/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "rex-central web UI") {
		t.Errorf("missing wiring-proof headline; body=%s", body)
	}
	if !strings.Contains(string(body), "1.2.3") {
		t.Errorf("version not surfaced; body=%s", body)
	}

	// /static/ must serve from internal/web's embed.FS.
	staticResp, err := http.Get(srv.URL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	defer staticResp.Body.Close()
	if staticResp.StatusCode != http.StatusOK {
		t.Fatalf("static status: %d", staticResp.StatusCode)
	}
	if ct := staticResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("static content-type=%q (want text/css)", ct)
	}
}

// TestLoginRendersChallengePackage exercises the /login handler:
// when Auth is wired, the page embeds the encoded package and the
// CLI command snippet, and Redirect comes from the ?redirect query.
func TestLoginRendersChallengePackage(t *testing.T) {
	t.Parallel()
	exp := time.Now().UTC().Add(time.Minute).Round(time.Second)
	auth := &stubAuth{pkg: proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: "ch-test",
		Nonce:       "deadbeef",
		ExpiresAt:   exp,
	}}
	s, err := New(Options{Version: "1.2.3", Auth: auth})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/login?redirect=/orgs/abc")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "rex remote login") {
		t.Errorf("missing CLI command snippet; body=%s", html)
	}
	if !strings.Contains(html, "--challenge") {
		t.Errorf("missing --challenge marker; body=%s", html)
	}
	if !strings.Contains(html, "/orgs/abc") {
		t.Errorf("redirect not surfaced; body=%s", html)
	}
	// The rendered command must contain a decodable challenge
	// package whose Redirect matches the query — confirms the
	// handler stamps the redirect through to the CLI.
	start := strings.Index(html, `--challenge &quot;`)
	if start < 0 {
		t.Fatalf("could not locate challenge token in body=%s", html)
	}
	start += len(`--challenge &quot;`)
	end := strings.Index(html[start:], `&quot;`)
	if end < 0 {
		t.Fatalf("could not locate end of challenge token")
	}
	wire := html[start : start+end]
	pkg, err := proto.DecodeLoginChallengePackage(wire)
	if err != nil {
		t.Fatalf("decode rendered package: %v (wire=%q)", err, wire)
	}
	if pkg.Redirect != "/orgs/abc" {
		t.Errorf("redirect not encoded: got %q want /orgs/abc", pkg.Redirect)
	}
	if pkg.ChallengeID != "ch-test" {
		t.Errorf("challenge id mismatch: got %q", pkg.ChallengeID)
	}
}

// TestLoginWithoutAuthReturns503 documents the misconfigured-
// deployment path: --web is on but no Auth was supplied. We surface
// 503 so an operator notices, rather than rendering a half-working
// /login that issues invalid challenges.
func TestLoginWithoutAuthReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (want 503)", resp.StatusCode)
	}
}

// TestLoginPropagatesAuthError covers the surfaceable-error branch:
// an Auth that fails to issue should return 500, not a silent 200
// with a broken package.
func TestLoginPropagatesAuthError(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Auth: &stubAuth{err: errors.New("rate limited")}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: %d (want 500)", resp.StatusCode)
	}
}

// TestUnknownPathReturns404 documents the catchall behaviour: a
// request to a path the web shell does not own returns 404, not a
// silent OK. When central-web is mounted as a fallback on the API
// mux, this is the boundary that keeps unknown API typos from
// rendering an HTML success page.
func TestUnknownPathReturns404(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}
