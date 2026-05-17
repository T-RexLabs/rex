package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	internalweb "github.com/asabla/rex/internal/web"
)

// stubRedeemer is a deterministic InviteRedeemer for the
// invite-handler tests. peekErr / redeemErr inject sentinels;
// peekOK / redeemOK supply the happy-path return values; calls
// capture each method invocation for post-assertion.
type stubRedeemer struct {
	peekOK     internalweb.InviteSummary
	peekErr    error
	redeemOK   internalweb.RedeemOutcome
	redeemErr  error
	peekCalls  []string
	redeemReqs []internalweb.RedeemRequest
}

func (s *stubRedeemer) PeekInvite(token string) (internalweb.InviteSummary, error) {
	s.peekCalls = append(s.peekCalls, token)
	if s.peekErr != nil {
		return internalweb.InviteSummary{}, s.peekErr
	}
	out := s.peekOK
	out.Token = token
	return out, nil
}

func (s *stubRedeemer) RedeemInvite(req internalweb.RedeemRequest) (internalweb.RedeemOutcome, error) {
	s.redeemReqs = append(s.redeemReqs, req)
	if s.redeemErr != nil {
		return internalweb.RedeemOutcome{}, s.redeemErr
	}
	return s.redeemOK, nil
}

// newRedeemerServer builds a Server with the supplied Redeemer
// and no Auth — invite routes are public so the session gate
// shouldn't trip in tests either way.
func newRedeemerServer(t *testing.T, r internalweb.InviteRedeemer) *httptest.Server {
	t.Helper()
	s, err := New(Options{Version: "test", Redeemer: r})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestInvitePeekRendersFormWithOrgAndRole covers the GET happy
// path: the form lands with the invite's org/role on the page
// and a hidden token input pointing at the requested token.
func TestInvitePeekRendersFormWithOrgAndRole(t *testing.T) {
	t.Parallel()
	r := &stubRedeemer{peekOK: internalweb.InviteSummary{
		OrgID:     "acme",
		Role:      "viewer",
		InvitedBy: "fp-alice",
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour).UTC(),
	}}
	srv := newRedeemerServer(t, r)
	resp, err := http.Get(srv.URL + "/invites/tok-abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "viewer") || !strings.Contains(html, "acme") {
		t.Errorf("page missing org/role: %s", html)
	}
	if !strings.Contains(html, `name="token" value="tok-abc"`) {
		t.Errorf("hidden token input missing: %s", html)
	}
	if !strings.Contains(html, `action="/invites/redeem"`) {
		t.Errorf("form action missing: %s", html)
	}
}

// TestInvitePeekShowsErrorOnSentinel covers the three sentinel
// branches: unknown / expired / already-redeemed each render the
// error page with the corresponding status code.
func TestInvitePeekShowsErrorOnSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"unknown", internalweb.ErrInviteNotFound, http.StatusNotFound},
		{"expired", internalweb.ErrInviteExpired, http.StatusGone},
		{"already-redeemed", internalweb.ErrInviteAlreadyRedeemed, http.StatusConflict},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newRedeemerServer(t, &stubRedeemer{peekErr: tc.err})
			resp, err := http.Get(srv.URL + "/invites/whatever")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status: got %d want %d", resp.StatusCode, tc.wantCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "not valid") {
				t.Errorf("error page body missing: %s", body)
			}
		})
	}
}

// TestInviteRedeemHappyPath covers the POST flow: stub redeem
// returns success; handler renders the success page with the
// fingerprint and a link back to /login.
func TestInviteRedeemHappyPath(t *testing.T) {
	t.Parallel()
	r := &stubRedeemer{redeemOK: internalweb.RedeemOutcome{
		OrgID:       "acme",
		Fingerprint: "fp-bob",
		Role:        "member",
	}}
	srv := newRedeemerServer(t, r)
	resp, err := http.PostForm(srv.URL+"/invites/redeem", url.Values{
		"token":          {"tok-abc"},
		"handle":         {"bob"},
		"public_key_pem": {"-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----"},
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "fp-bob") || !strings.Contains(html, "member") {
		t.Errorf("success page missing fingerprint/role: %s", html)
	}
	if !strings.Contains(html, `href="/login"`) {
		t.Errorf("success page missing /login link: %s", html)
	}
	if len(r.redeemReqs) != 1 {
		t.Fatalf("redeem calls: %d want 1", len(r.redeemReqs))
	}
	req := r.redeemReqs[0]
	if req.Token != "tok-abc" || req.Handle != "bob" {
		t.Errorf("redeem req: %+v", req)
	}
}

// TestInviteRedeemSentinelShowsErrorPage covers the
// redeem-side sentinel: the underlying redeem fails with an
// AlreadyRedeemed sentinel and the handler renders the error
// page rather than the success page.
func TestInviteRedeemSentinelShowsErrorPage(t *testing.T) {
	t.Parallel()
	r := &stubRedeemer{redeemErr: internalweb.ErrInviteAlreadyRedeemed}
	srv := newRedeemerServer(t, r)
	resp, err := http.PostForm(srv.URL+"/invites/redeem", url.Values{
		"token":          {"tok-old"},
		"public_key_pem": {"-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----"},
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status: %d want 409", resp.StatusCode)
	}
}

// TestInviteRedeemReRendersFormOnNonSentinelError covers the
// "PEM typo" branch: a non-sentinel error (e.g. parse failure)
// re-renders the form with the error inline so the recipient can
// correct without chasing a fresh invite. PeekInvite is called
// a second time to refill org/role context.
func TestInviteRedeemReRendersFormOnNonSentinelError(t *testing.T) {
	t.Parallel()
	r := &stubRedeemer{
		peekOK:    internalweb.InviteSummary{OrgID: "acme", Role: "member", ExpiresAt: time.Now().Add(24 * time.Hour).UTC()},
		redeemErr: io.EOF, // arbitrary non-sentinel error stand-in
	}
	srv := newRedeemerServer(t, r)
	resp, err := http.PostForm(srv.URL+"/invites/redeem", url.Values{
		"token":          {"tok-abc"},
		"public_key_pem": {"not a pem"},
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d want 200 (re-rendered form)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "Could not redeem") {
		t.Errorf("error banner missing on re-render: %s", html)
	}
	if !strings.Contains(html, `action="/invites/redeem"`) {
		t.Errorf("form missing on re-render: %s", html)
	}
	if len(r.peekCalls) != 1 {
		t.Errorf("expected one peek call after non-sentinel error, got %d", len(r.peekCalls))
	}
}

// TestInviteRoutesArePublic covers the unauthenticated carve-out
// — the routes work even when an Auth is bound (the session gate
// is configured but the path bypasses it).
func TestInviteRoutesArePublic(t *testing.T) {
	t.Parallel()
	r := &stubRedeemer{peekOK: internalweb.InviteSummary{OrgID: "acme", Role: "viewer", ExpiresAt: time.Now().Add(24 * time.Hour).UTC()}}
	s, err := New(Options{
		Version:  "test",
		Auth:     &stubAuth{},
		Redeemer: r,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/invites/tok-abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/invites/<token>: %d (want 200; route should bypass session gate)", resp.StatusCode)
	}
	// POST /invites/redeem also public.
	resp2, err := http.PostForm(srv.URL+"/invites/redeem", url.Values{
		"token":          {"tok-abc"},
		"public_key_pem": {"-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----"},
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("/invites/redeem: %d (want 200; route should bypass session gate)", resp2.StatusCode)
	}
}

// TestInviteHandlerUnconfiguredReturns503 covers the
// dev-deployment branch: when Redeemer is nil (e.g. --db not on),
// the routes 503 with a clear message.
func TestInviteHandlerUnconfiguredReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/invites/tok-abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/invites/<token>: %d want 503", resp.StatusCode)
	}
}
