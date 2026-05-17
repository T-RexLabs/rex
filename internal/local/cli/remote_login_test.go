package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// testCentralStub is the minimal HTTP surface runRemoteLogin needs
// in tests: POST /auth/challenge (used when --challenge is omitted)
// and POST /auth/verify (verifies the ed25519 signature against
// the registered pubkey and returns a deterministic access token).
type testCentralStub struct {
	pub       ed25519.PublicKey
	hostname  string
	issuedID  string
	issuedExp time.Time
	token     string
}

func newCentralStub(t *testing.T, pub ed25519.PublicKey) (*testCentralStub, *httptest.Server) {
	t.Helper()
	stub := &testCentralStub{
		pub:       pub,
		hostname:  "central.test",
		issuedID:  "ch-stub",
		issuedExp: time.Now().UTC().Add(time.Minute).Round(time.Second),
		token:     "tok-stub-aaaabbbbccccdddd",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/challenge", stub.handleChallenge)
	mux.HandleFunc("/auth/verify", stub.handleVerify)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return stub, srv
}

func (s *testCentralStub) handleChallenge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(proto.AuthChallengeResponse{
		ChallengeID: s.issuedID,
		Nonce:       "abcdef0123456789abcdef0123456789",
		Hostname:    s.hostname,
		ExpiresAt:   s.issuedExp,
	})
}

func (s *testCentralStub) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req proto.AuthVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Reconstruct the canonical signing input and verify against
	// the registered pubkey. The nonce comes from the same stub
	// challenge handler above; the test that uses --challenge
	// crafts its own package and so passes a different nonce — we
	// don't hardcode the value here, instead trusting the request
	// shape and verifying the ed25519 signature on whatever
	// canonical input matches the package the CLI built.
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    findStubNonceFor(req.ChallengeID),
		Hostname: s.hostname,
		Scope:    req.Scope,
	})
	sig, err := hex.DecodeString(req.Signature)
	if err != nil {
		http.Error(w, "bad sig hex", http.StatusBadRequest)
		return
	}
	if !ed25519.Verify(s.pub, canonical, sig) {
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(proto.AuthVerifyResponse{
		AccessToken: s.token,
		ExpiresAt:   time.Now().UTC().Add(15 * time.Minute),
	})
}

// stubNonces is the table of {challenge id → hex nonce} the stub
// recognises. Tests register entries via withStubChallenge so the
// signature-verification path lines up with whatever package the
// CLI signed. The mutex guards concurrent access — the writer is
// the test goroutine calling withStubChallenge; the reader is
// the stub server's handleVerify goroutine on the httptest server
// (a CI race-detector flake when several t.Parallel tests share
// the package's stub).
var (
	stubNoncesMu sync.RWMutex
	stubNonces   = map[string]string{}
)

func withStubChallenge(t *testing.T, id, nonceHex string) {
	t.Helper()
	stubNoncesMu.Lock()
	stubNonces[id] = nonceHex
	stubNoncesMu.Unlock()
	t.Cleanup(func() {
		stubNoncesMu.Lock()
		delete(stubNonces, id)
		stubNoncesMu.Unlock()
	})
}

func findStubNonceFor(id string) string {
	stubNoncesMu.RLock()
	v, ok := stubNonces[id]
	stubNoncesMu.RUnlock()
	if ok {
		return v
	}
	// Fall back to the stub server's /auth/challenge default
	// nonce so the fresh-handshake path (no --challenge) still
	// validates without an explicit register call.
	return "abcdef0123456789abcdef0123456789"
}

// newTestSigner mints a fresh ed25519 keypair and wraps it in a
// MemorySigner. Returns both the signer and its raw public key so
// tests can register it with the stub central.
func newTestSigner(t *testing.T) (identity.Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	kp := identity.Keypair{
		Handle:  "tester",
		Public:  pub,
		Private: priv,
	}
	signer, err := identity.NewMemorySigner(kp)
	if err != nil {
		t.Fatalf("NewMemorySigner: %v", err)
	}
	return signer, pub
}

// TestRunRemoteLoginWithChallenge is the happy path for the CLI's
// --challenge flow: the user pastes the encoded package from the
// /login page, the CLI signs the package's nonce, gets a token,
// and assembles a redeem URL that targets the right central with
// the embedded redirect.
func TestRunRemoteLoginWithChallenge(t *testing.T) {
	t.Parallel()
	signer, pub := newTestSigner(t)
	_, hs := newCentralStub(t, pub)

	nonceHex := "11aa22bb33cc44dd55ee66ff7700aabb"
	withStubChallenge(t, "ch-with-challenge", nonceHex)
	pkg := proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: "ch-with-challenge",
		Nonce:       nonceHex,
		Hostname:    "central.test",
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
		Redirect:    "/orgs/acme/workspaces/ws-1",
	}
	wire, err := proto.EncodeLoginChallengePackage(pkg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	res, err := runRemoteLogin(context.Background(), hs.URL, signer, wire, "", "sync", hs.Client())
	if err != nil {
		t.Fatalf("runRemoteLogin: %v", err)
	}

	u, err := url.Parse(res.RedeemURL)
	if err != nil {
		t.Fatalf("parse redeem url: %v", err)
	}
	if u.Path != "/auth/redeem" {
		t.Errorf("redeem path: %q", u.Path)
	}
	q := u.Query()
	if q.Get("token") != "tok-stub-aaaabbbbccccdddd" {
		t.Errorf("token query: %q", q.Get("token"))
	}
	if q.Get("redirect") != "/orgs/acme/workspaces/ws-1" {
		t.Errorf("redirect not threaded through: %q", q.Get("redirect"))
	}
	if res.AccessTokenExpiresAt.IsZero() {
		t.Error("expires_at not surfaced")
	}
}

// TestRunRemoteLoginFreshHandshake covers the no-challenge path:
// CLI fetches a challenge via POST /auth/challenge, signs it, and
// assembles a redeem URL with the default "/" redirect.
func TestRunRemoteLoginFreshHandshake(t *testing.T) {
	t.Parallel()
	signer, pub := newTestSigner(t)
	_, hs := newCentralStub(t, pub)

	res, err := runRemoteLogin(context.Background(), hs.URL, signer, "", "", "sync", hs.Client())
	if err != nil {
		t.Fatalf("runRemoteLogin: %v", err)
	}
	u, _ := url.Parse(res.RedeemURL)
	if u.Query().Get("redirect") != "/" {
		t.Errorf("default redirect: got %q want /", u.Query().Get("redirect"))
	}
}

// TestRunRemoteLoginRedirectOverride confirms --redirect wins
// against the package's embedded Redirect.
func TestRunRemoteLoginRedirectOverride(t *testing.T) {
	t.Parallel()
	signer, pub := newTestSigner(t)
	_, hs := newCentralStub(t, pub)
	nonceHex := "abababababababababababababababab"
	withStubChallenge(t, "ch-override", nonceHex)
	pkg := proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: "ch-override",
		Nonce:       nonceHex,
		Hostname:    "central.test",
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
		Redirect:    "/from-package",
	}
	wire, _ := proto.EncodeLoginChallengePackage(pkg)
	res, err := runRemoteLogin(context.Background(), hs.URL, signer, wire, "/from-override", "sync", hs.Client())
	if err != nil {
		t.Fatalf("runRemoteLogin: %v", err)
	}
	u, _ := url.Parse(res.RedeemURL)
	if got := u.Query().Get("redirect"); got != "/from-override" {
		t.Errorf("redirect: got %q want /from-override", got)
	}
}

// TestRunRemoteLoginRejectsExpired confirms a stale --challenge
// fails locally with a clear error before bothering the server.
func TestRunRemoteLoginRejectsExpired(t *testing.T) {
	t.Parallel()
	signer, pub := newTestSigner(t)
	_, hs := newCentralStub(t, pub)
	pkg := proto.LoginChallengePackage{
		Version:     proto.LoginChallengePackageVersion,
		ChallengeID: "ch-stale",
		Nonce:       "00",
		Hostname:    "central.test",
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
	}
	wire, _ := proto.EncodeLoginChallengePackage(pkg)
	_, err := runRemoteLogin(context.Background(), hs.URL, signer, wire, "", "sync", hs.Client())
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want expired error, got %v", err)
	}
}

// TestRunRemoteLoginRejectsExternalRedirect blocks an attacker-
// supplied redirect from leaving the central origin.
func TestRunRemoteLoginRejectsExternalRedirect(t *testing.T) {
	t.Parallel()
	signer, _ := newTestSigner(t)
	_, err := runRemoteLogin(context.Background(), "http://central.test", signer, "", "https://evil.test", "sync", &http.Client{})
	if err == nil || !strings.Contains(err.Error(), "same-origin") {
		t.Fatalf("want same-origin error, got %v", err)
	}
}

// TestRunRemoteLoginPropagatesServerError surfaces /auth/verify's
// body in the returned error so the operator sees the central's
// diagnostic, not just an opaque 401.
func TestRunRemoteLoginPropagatesServerError(t *testing.T) {
	t.Parallel()
	signer, _ := newTestSigner(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/challenge", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(proto.AuthChallengeResponse{
			ChallengeID: "ch-deny",
			Nonce:       "0011",
			Hostname:    "central.test",
			ExpiresAt:   time.Now().UTC().Add(time.Minute),
		})
	})
	mux.HandleFunc("/auth/verify", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unknown-identity"}`)
	})
	hs := httptest.NewServer(mux)
	defer hs.Close()
	_, err := runRemoteLogin(context.Background(), hs.URL, signer, "", "", "sync", hs.Client())
	if err == nil || !strings.Contains(err.Error(), "unknown-identity") {
		t.Fatalf("want server diagnostic in error, got %v", err)
	}
}

// TestRemoteLoginCobraAcceptsInlineURL covers the cobra layer:
// the 2-arg form `rex remote login <name> <url>` is accepted
// (regression — the previous `cobra.ExactArgs(1)` rejected the
// natural `rex remote login dev http://127.0.0.1:8080` form
// that `make web-dev` users would type). The test doesn't
// assert end-to-end success against a stub central — it just
// pins that the args parse + dispatch into the runRemoteLogin
// path. A network error against the bogus URL is the expected
// outcome.
func TestRemoteLoginCobraAcceptsInlineURL(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "login", "dev", "http://127.0.0.1:1",
		"--remotes-file", reg,
		"--print-url",
	)
	if err == nil {
		t.Fatal("expected handshake failure against unreachable URL")
	}
	if strings.Contains(err.Error(), "accepts") || strings.Contains(err.Error(), "arg(s)") {
		t.Fatalf("cobra arg validation rejected the 2-arg form: %v", err)
	}
}

// TestRemoteLoginOneArgWithoutRegistrationErrors is the
// companion: the 1-arg form still requires the remote to be
// registered, with the error message pointing at the
// inline-URL escape hatch.
func TestRemoteLoginOneArgWithoutRegistrationErrors(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "login", "ghost",
		"--remotes-file", reg,
	)
	if err == nil {
		t.Fatal("login against unregistered remote without inline URL should error")
	}
	if !strings.Contains(err.Error(), "not registered") || !strings.Contains(err.Error(), "<url>") {
		t.Fatalf("error wording missing inline-URL hint: %v", err)
	}
}

// TestBuildRedeemURLEdgeCases keeps the URL composer honest: it
// must reject malformed base URLs and trailing-slash quirks.
func TestBuildRedeemURLEdgeCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		base   string
		want   string
		wantOK bool
	}{
		{"plain", "http://central.test", "http://central.test/auth/redeem?redirect=%2F&token=tok", true},
		{"trailing slash", "http://central.test/", "http://central.test/auth/redeem?redirect=%2F&token=tok", true},
		{"no scheme", "central.test", "", false},
		{"https with path", "https://central.test/base", "https://central.test/base/auth/redeem?redirect=%2F&token=tok", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildRedeemURL(tc.base, "tok", "/")
			if tc.wantOK && err != nil {
				t.Fatalf("err: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("expected error for %q", tc.base)
			}
			if tc.wantOK && got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
