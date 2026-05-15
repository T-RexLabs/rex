package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// TestLoginChallengePackageRoundtrip covers Encode → Decode and the
// version guard. A package with a different Version must refuse to
// decode so a CLI built against an older shape fails loudly rather
// than mis-signing.
func TestLoginChallengePackageRoundtrip(t *testing.T) {
	t.Parallel()
	pkg := proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: "ch-abc",
		Nonce:       "deadbeef",
		Hostname:    "central.example",
		ExpiresAt:   time.Now().UTC().Add(time.Minute).Round(time.Second),
	}
	wire, err := proto.EncodeLoginChallengePackage(pkg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := proto.DecodeLoginChallengePackage(wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ChallengeID != pkg.ChallengeID || got.Nonce != pkg.Nonce ||
		got.Hostname != pkg.Hostname || !got.ExpiresAt.Equal(pkg.ExpiresAt) {
		t.Fatalf("roundtrip mismatch:\n want %+v\n  got %+v", pkg, got)
	}

	// Version mismatch must refuse cleanly.
	bad := pkg
	bad.Version = "rex-login-v0"
	bw, _ := proto.EncodeLoginChallengePackage(bad)
	if _, err := proto.DecodeLoginChallengePackage(bw); err == nil {
		t.Fatal("decode accepted wrong version; want error")
	}
}

// TestIssueLoginChallengeProducesUsableNonce confirms the package
// the /login surface hands the CLI carries the same data the
// challenge resolver expects, by verifying the ID round-trips
// through consumeChallenge (the same path /auth/verify takes).
func TestIssueLoginChallengeProducesUsableNonce(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	pkg, err := s.IssueLoginChallenge("central.example")
	if err != nil {
		t.Fatalf("IssueLoginChallenge: %v", err)
	}
	if pkg.Hostname != "central.example" {
		t.Errorf("hostname not stamped: %q", pkg.Hostname)
	}
	if pkg.ExpiresAt.Before(time.Now()) {
		t.Errorf("issued challenge already expired: %s", pkg.ExpiresAt)
	}
	// The challenge is reachable via the existing consumeChallenge
	// path; consuming it twice errors (single-use semantics
	// inherited from authState).
	if _, err := s.auth.consumeChallenge(pkg.ChallengeID); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if _, err := s.auth.consumeChallenge(pkg.ChallengeID); err == nil {
		t.Fatal("second consume succeeded; want error")
	}
}

// TestAuthRedeemSetsCookieAndRedirects exercises the happy path: a
// valid token → 303 with Set-Cookie carrying the same value, plus
// the Secure / HttpOnly / SameSite attributes (CENTRAL-AUTH.1).
func TestAuthRedeemSetsCookieAndRedirects(t *testing.T) {
	t.Parallel()
	s, hs := newTestServer(t)
	fp := newTestFingerprint(t)
	tok, err := s.auth.issueToken(fp, "sync")
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	value := tok.legacyValue
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(hs.URL + authRedeemPath + "?token=" + value + "&redirect=/specs")
	if err != nil {
		t.Fatalf("GET redeem: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d (want 303)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/specs" {
		t.Errorf("Location: %q (want /specs)", loc)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("rex_session cookie not set")
	}
	if session.Value != value {
		t.Errorf("cookie value mismatch (want token roundtrip)")
	}
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie attrs: HttpOnly=%t Secure=%t SameSite=%v (want all strict)",
			session.HttpOnly, session.Secure, session.SameSite)
	}
}

// TestAuthRedeemRejectsBadToken keeps a 401 boundary on /auth/redeem
// so a stray browser request with an arbitrary ?token= cannot land
// on the protected surface.
func TestAuthRedeemRejectsBadToken(t *testing.T) {
	t.Parallel()
	_, hs := newTestServer(t)
	resp, err := http.Get(hs.URL + authRedeemPath + "?token=nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d (want 401)", resp.StatusCode)
	}
}

// TestAuthRedeemRejectsOpenRedirect confirms the same-origin-only
// guard against //evil.test, https://evil.test, and the like.
func TestAuthRedeemRejectsOpenRedirect(t *testing.T) {
	t.Parallel()
	s, hs := newTestServer(t)
	fp := newTestFingerprint(t)
	tok, _ := s.auth.issueToken(fp, "sync")
	for _, redirect := range []string{
		"//evil.test/path",
		"https://evil.test/path",
		"javascript:alert(1)",
		"specs", // missing leading slash
	} {
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, err := client.Get(hs.URL + authRedeemPath + "?token=" + tok.legacyValue + "&redirect=" + redirect)
		if err != nil {
			t.Fatalf("GET %s: %v", redirect, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("redirect=%q status=%d (want 400)", redirect, resp.StatusCode)
		}
	}
}

// TestAuthLogoutClearsCookieAndRevokesToken proves the two side
// effects of /auth/logout: the cookie comes back with MaxAge=-1
// (which makes browsers drop it) and the underlying token no
// longer resolves on the resolver.
func TestAuthLogoutClearsCookieAndRevokesToken(t *testing.T) {
	t.Parallel()
	s, hs := newTestServer(t)
	fp := newTestFingerprint(t)
	tok, _ := s.auth.issueToken(fp, "sync")

	req, _ := http.NewRequest(http.MethodPost, hs.URL+authLogoutPath, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok.legacyValue})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST logout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: %d (want 204)", resp.StatusCode)
	}
	var cleared *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatal("expected Set-Cookie clearing rex_session")
	}
	if cleared.MaxAge >= 0 {
		t.Errorf("MaxAge=%d (want negative for clear)", cleared.MaxAge)
	}
	if _, err := s.auth.resolveAccessToken(tok.legacyValue); err == nil {
		t.Fatal("token still resolves after logout; want error")
	}
}

// TestRequireTokenAcceptsCookie is the CENTRAL-AUTH.3 invariant in
// one assertion: requireToken returns the bound fingerprint when
// the token rides in the rex_session cookie instead of the
// Authorization header.
func TestRequireTokenAcceptsCookie(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	fp := newTestFingerprint(t)
	tok, _ := s.auth.issueToken(fp, "sync")

	req := httptest.NewRequest(http.MethodGet, "/sync/state", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok.legacyValue})
	got, err := s.requireToken(req)
	if err != nil {
		t.Fatalf("requireToken (cookie): %v", err)
	}
	if got.String() != fp.String() {
		t.Errorf("fingerprint mismatch: got=%s want=%s", got, fp)
	}
}

// TestIsSafeRedirect documents the same-origin path matcher.
func TestIsSafeRedirect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"/specs", true},
		{"/orgs/abc/workspaces/xyz/specs", true},
		{"", false},
		{"specs", false},        // missing leading slash
		{"//evil.test", false},  // protocol-relative
		{"/\\evil.test", false}, // back-slashed
		{"https://evil.test", false},
		{"javascript:alert(1)", false},
	}
	for _, tc := range cases {
		if got := isSafeRedirect(tc.in); got != tc.want {
			t.Errorf("isSafeRedirect(%q) = %t (want %t)", tc.in, got, tc.want)
		}
	}
}

// newTestFingerprint generates a fresh ed25519 keypair and returns
// its fingerprint. Used to mint tokens directly via authState in
// tests that don't need the full keystore handshake.
func newTestFingerprint(t *testing.T) identity.Fingerprint {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 generate: %v", err)
	}
	fp, err := identity.FingerprintOf(pub)
	if err != nil {
		t.Fatalf("FingerprintOf: %v", err)
	}
	return fp
}
