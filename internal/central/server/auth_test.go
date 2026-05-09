package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

func postJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, respBody
}

func TestAuthChallengeReturnsNonce(t *testing.T) {
	t.Parallel()

	_, hs, _ := newSignedTestServer(t, "alice")
	resp, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	var ch proto.AuthChallengeResponse
	if err := json.Unmarshal(body, &ch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ch.ChallengeID == "" {
		t.Fatal("missing challenge_id")
	}
	if len(ch.Nonce) != 64 { // 32 bytes hex-encoded
		t.Fatalf("nonce length: got %d want 64", len(ch.Nonce))
	}
	if ch.Hostname == "" {
		t.Fatal("hostname should be set")
	}
	if ch.ExpiresAt.Before(time.Now()) {
		t.Fatal("expires_at should be in the future")
	}
}

func TestAuthVerifyHappyPath(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	priv := privs["alice"]
	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)

	// Get challenge.
	resp, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("challenge: %d %s", resp.StatusCode, body)
	}
	var ch proto.AuthChallengeResponse
	_ = json.Unmarshal(body, &ch)

	// Sign.
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version: proto.AuthSigningVersion, Nonce: ch.Nonce,
		Hostname: ch.Hostname, Scope: "sync",
	})
	sig := ed25519.Sign(priv, canonical)

	// Verify.
	resp, body = postJSON(t, hs.URL+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(sig),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: %d %s", resp.StatusCode, body)
	}
	var v proto.AuthVerifyResponse
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	if v.AccessToken == "" {
		t.Fatal("missing access_token")
	}
	if v.ExpiresAt.Before(time.Now()) {
		t.Fatal("expires_at should be in the future")
	}
}

func TestAuthVerifyRejectsTamperedSignature(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	priv := privs["alice"]
	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)

	_, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	var ch proto.AuthChallengeResponse
	_ = json.Unmarshal(body, &ch)

	// Sign the WRONG canonical input (changed scope) and submit
	// claiming sync scope; server reconstructs with sync scope and
	// verification fails.
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version: proto.AuthSigningVersion, Nonce: ch.Nonce,
		Hostname: ch.Hostname, Scope: "different",
	})
	sig := ed25519.Sign(priv, canonical)

	resp, body := postJSON(t, hs.URL+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(sig),
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestAuthVerifyRejectsUnregisteredFingerprint(t *testing.T) {
	t.Parallel()

	_, hs, _ := newSignedTestServer(t, "alice")
	// bob is not registered.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fp, _ := identity.FingerprintOf(pub)

	_, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	var ch proto.AuthChallengeResponse
	_ = json.Unmarshal(body, &ch)

	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version: proto.AuthSigningVersion, Nonce: ch.Nonce,
		Hostname: ch.Hostname, Scope: "sync",
	})
	sig := ed25519.Sign(priv, canonical)

	resp, body := postJSON(t, hs.URL+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(sig),
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestAuthVerifyChallengeReuseRejected(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	priv := privs["alice"]
	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)

	// Issue a challenge.
	_, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	var ch proto.AuthChallengeResponse
	_ = json.Unmarshal(body, &ch)

	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version: proto.AuthSigningVersion, Nonce: ch.Nonce,
		Hostname: ch.Hostname, Scope: "sync",
	})
	sig := ed25519.Sign(priv, canonical)
	verify := proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(sig),
	}

	// First use succeeds.
	resp, body := postJSON(t, hs.URL+authVerifyPath, verify)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first verify: %d %s", resp.StatusCode, body)
	}
	// Second use of the same challenge id is rejected.
	resp, body = postJSON(t, hs.URL+authVerifyPath, verify)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay: %d %s", resp.StatusCode, body)
	}
}

func TestAuthVerifyExpiredChallengeRejected(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	// Force the auth state's clock backwards so the issued
	// challenge is treated as already expired by consumeChallenge.
	srv.auth.now = func() time.Time {
		return time.Now().Add(2 * time.Minute)
	}

	priv := privs["alice"]
	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)

	// Issue under one clock; consume under future-clock.
	_, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	var ch proto.AuthChallengeResponse
	_ = json.Unmarshal(body, &ch)

	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version: proto.AuthSigningVersion, Nonce: ch.Nonce,
		Hostname: ch.Hostname, Scope: "sync",
	})
	sig := ed25519.Sign(priv, canonical)

	// Move the clock further forward so the challenge has expired
	// (issued at "+2min"; expires_at is "+3min"; we set time to
	// "+5min").
	srv.auth.now = func() time.Time { return time.Now().Add(5 * time.Minute) }

	resp, body := postJSON(t, hs.URL+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(sig),
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired: %d %s", resp.StatusCode, body)
	}
}

func TestSyncEventsRequiresBearerWhenKeystoreSet(t *testing.T) {
	t.Parallel()

	_, hs, _ := newSignedTestServer(t, "alice")
	resp, body := postJSON(t, hs.URL+"/sync/events", proto.PushRequest{Since: "", Events: nil})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestSyncEventsAllowsBearerWhenValid(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	token := issueTestToken(t, srv, privs["alice"])

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/sync/events", bytes.NewReader([]byte(`{"since":"","events":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestSyncEventsRejectsRevokedToken(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	token := issueTestToken(t, srv, privs["alice"])

	// Revoke directly via the auth state. Tokens are stored by
	// hash (TOKEN.2) so look up via hashToken on the wire value.
	srv.auth.mu.Lock()
	srv.auth.tokens[hashToken(token)].revoked = true
	srv.auth.mu.Unlock()

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/sync/events", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}
