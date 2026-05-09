package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// completeAuth runs the full /auth/challenge → /auth/verify dance
// using the per-test signed server fixture and returns the issued
// token pair. Mirrors what a production client does on first
// connect; gives token-lifecycle tests a single helper.
func completeAuth(t *testing.T, hs interface{ URL() string }, priv ed25519.PrivateKey, scope string) proto.AuthVerifyResponse {
	t.Helper()
	if scope == "" {
		scope = "sync"
	}

	resp, body := postJSON(t, hs.URL()+authChallengePath, struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("challenge: %d %s", resp.StatusCode, body)
	}
	var ch proto.AuthChallengeResponse
	if err := json.Unmarshal(body, &ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    ch.Nonce,
		Hostname: ch.Hostname,
		Scope:    scope,
	})
	sig := ed25519.Sign(priv, canonical)

	resp, body = postJSON(t, hs.URL()+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID, Fingerprint: fp.String(),
		Scope: scope, Signature: hex.EncodeToString(sig),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: %d %s", resp.StatusCode, body)
	}
	var v proto.AuthVerifyResponse
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	return v
}

// urlHolder lets completeAuth take either an *httptest.Server (which
// has URL as a field) via this thin adapter — the test fixture
// returns one.
type urlHolder struct{ url string }

func (u urlHolder) URL() string { return u.url }

func TestVerifyIssuesAccessAndRefreshPair(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	v := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")
	if v.AccessToken == "" {
		t.Fatal("AccessToken should be present")
	}
	if v.RefreshToken == "" {
		t.Fatal("RefreshToken should be present (TOKEN.1)")
	}
	if v.RefreshToken == v.AccessToken {
		t.Fatal("refresh and access tokens must be distinct")
	}
	if !v.RefreshExpiresAt.After(v.ExpiresAt) {
		t.Errorf("refresh expiry should be > access expiry: refresh=%s access=%s",
			v.RefreshExpiresAt, v.ExpiresAt)
	}
}

func TestVerifyStoresHashesNotRawTokens(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	v := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	// The wire value must NOT appear as a key in the auth state's
	// tokens map. Only the SHA-256 hash should.
	srv.auth.mu.Lock()
	defer srv.auth.mu.Unlock()
	for k := range srv.auth.tokens {
		if k == v.AccessToken || k == v.RefreshToken {
			t.Fatalf("raw wire token %q stored as key — TOKEN.2 violated", k)
		}
	}
	if _, ok := srv.auth.tokens[hashToken(v.AccessToken)]; !ok {
		t.Fatal("access token hash should be in tokens map")
	}
	if _, ok := srv.auth.tokens[hashToken(v.RefreshToken)]; !ok {
		t.Fatal("refresh token hash should be in tokens map")
	}
}

func TestRefreshRotatesPair(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	first := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	resp, body := postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: first.RefreshToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh status: %d body: %s", resp.StatusCode, body)
	}
	var rotated proto.AuthRefreshResponse
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rotated.AccessToken == first.AccessToken {
		t.Error("rotation should mint a new access token")
	}
	if rotated.RefreshToken == first.RefreshToken {
		t.Error("rotation should mint a new refresh token (TOKEN.3)")
	}
}

func TestRefreshReplayRevokesEntireChain(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	first := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	// First rotation legitimately succeeds.
	resp, body := postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: first.RefreshToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first rotate: %d %s", resp.StatusCode, body)
	}
	var rotated proto.AuthRefreshResponse
	_ = json.Unmarshal(body, &rotated)

	// Reuse the original refresh — SEC.2 chain revoke must fire.
	resp, body = postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: first.RefreshToken,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay should be rejected: %d %s", resp.StatusCode, body)
	}

	// All three tokens (original access, original refresh,
	// rotated access, rotated refresh) should be revoked. Use
	// resolveAccessToken on each access token: returns
	// ErrTokenInvalid because revoked=true.
	if _, err := srv.auth.resolveAccessToken(first.AccessToken); err == nil {
		t.Error("original access should be revoked after replay")
	}
	if _, err := srv.auth.resolveAccessToken(rotated.AccessToken); err == nil {
		t.Error("rotated access should be revoked after replay")
	}
}

func TestRevokeSingleToken(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	v := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	body, _ := json.Marshal(proto.AuthRevokeRequest{Token: v.AccessToken})
	req, _ := http.NewRequest(http.MethodPost, hs.URL+authRevokePath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+v.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, respBody)
	}
	var rr proto.AuthRevokeResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rr.Count != 1 {
		t.Fatalf("count: got %d want 1", rr.Count)
	}

	// Subsequent /sync/events with the revoked token must 401.
	pushReq, _ := http.NewRequest(http.MethodPost, hs.URL+"/sync/events", bytes.NewReader([]byte(`{}`)))
	pushReq.Header.Set("Authorization", "Bearer "+v.AccessToken)
	pushResp, _ := http.DefaultClient.Do(pushReq)
	if pushResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token should 401 on sync; got %d", pushResp.StatusCode)
	}
	pushResp.Body.Close()
}

func TestRevokeAll(t *testing.T) {
	t.Parallel()

	_, hs, privs := newSignedTestServer(t, "alice")
	v := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	body, _ := json.Marshal(proto.AuthRevokeRequest{All: true})
	req, _ := http.NewRequest(http.MethodPost, hs.URL+authRevokePath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+v.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("revoke all: %d body: %s", resp.StatusCode, respBody)
	}
	var rr proto.AuthRevokeResponse
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	if rr.Count < 2 {
		t.Errorf("revoke all should kill access + refresh: got %d", rr.Count)
	}

	// Both refresh and access should now be invalid.
	rrResp, body2 := postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: v.RefreshToken,
	})
	if rrResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after revoke-all should fail; got %d %s", rrResp.StatusCode, body2)
	}
}

func TestAccessTokenExpiry(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	// Pin the clock so we can age tokens artificially.
	clock := mustPinClock(t, srv)

	v := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	// Advance past the access TTL.
	clock.advance(accessTokenTTL + time.Second)

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/sync/events",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+v.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired access should 401; got %d", resp.StatusCode)
	}

	// But the refresh token (with 30d TTL) is still valid; the
	// client can reach a fresh access token without a fresh
	// handshake.
	rotResp, body := postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: v.RefreshToken,
	})
	if rotResp.StatusCode != http.StatusOK {
		t.Fatalf("refresh after access expiry: %d %s", rotResp.StatusCode, body)
	}
}

// pinnedClock injects a controllable now() into the auth state so
// expiry-related tests don't have to wait real wall time.
type pinnedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *pinnedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *pinnedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func mustPinClock(t *testing.T, srv *Server) *pinnedClock {
	t.Helper()
	clock := &pinnedClock{now: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)}
	srv.auth.mu.Lock()
	srv.auth.now = clock.Now
	srv.auth.mu.Unlock()
	return clock
}

// captureAuditAppender records every (eventType, payload) pair the
// server emits so tests can assert audit-log shape.
type captureAuditAppender struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	Type    string
	Payload any
}

func (c *captureAuditAppender) Append(_ context.Context, eventType string, payload any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, capturedEvent{Type: eventType, Payload: payload})
	return nil
}

func (c *captureAuditAppender) typesEmitted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i] = e.Type
	}
	return out
}

func TestAuthAuditEmitsLifecycleEvents(t *testing.T) {
	t.Parallel()

	cap := &captureAuditAppender{}
	srv, hs, privs := newSignedTestServerWithAudit(t, cap, "alice")
	v := completeAuth(t, urlHolder{hs.URL}, privs["alice"], "")

	// Refresh once.
	resp, body := postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: v.RefreshToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: %d %s", resp.StatusCode, body)
	}
	var rotated proto.AuthRefreshResponse
	_ = json.Unmarshal(body, &rotated)

	// Replay the original refresh — chain revoke fires.
	resp, _ = postJSON(t, hs.URL+authRefreshPath, proto.AuthRefreshRequest{
		RefreshToken: v.RefreshToken,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay should be denied: %d", resp.StatusCode)
	}

	// Confirm the captured events cover the spec contract.
	got := cap.typesEmitted()
	wantOnce := []string{
		"auth.success", "token.issued", "token.refreshed",
		"auth.replay_attempt", "token.revoked",
	}
	for _, ev := range wantOnce {
		if !containsString(got, ev) {
			t.Errorf("missing audit event %q in %v", ev, got)
		}
	}

	_ = srv // silence unused lint; srv exists for downstream assertions
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestAuthFailureEmitsAudit(t *testing.T) {
	t.Parallel()

	cap := &captureAuditAppender{}
	_, hs, _ := newSignedTestServerWithAudit(t, cap, "alice")

	// Bogus verify body — fails on unknown fingerprint.
	resp, _ := postJSON(t, hs.URL+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: "fake-id",
		Fingerprint: "ffffffffffffffff",
		Scope:       "sync",
		Signature:   "deadbeef",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401: %d", resp.StatusCode)
	}
	if !containsString(cap.typesEmitted(), "auth.failure") {
		t.Errorf("auth.failure missing: %v", cap.typesEmitted())
	}
}
