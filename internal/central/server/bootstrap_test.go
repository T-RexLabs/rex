package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// TestBootstrapTokenSeededOnFreshDB asserts schema step 5 seeds
// exactly one pending bootstrap token row when no admin exists.
// (central-node.BOOT.1)
func TestBootstrapTokenSeededOnFreshDB(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	tok, ok, err := s.LookupBootstrapToken(context.Background())
	if err != nil {
		t.Fatalf("LookupBootstrapToken: %v", err)
	}
	if !ok {
		t.Fatal("expected a seeded token on a fresh DB")
	}
	if tok.Token == "" {
		t.Fatal("token: empty")
	}
	if !tok.Pending() {
		t.Fatal("expected token to be pending")
	}
	if tok.RedeemedBy != "" {
		t.Fatalf("redeemed_by: %q (want empty)", tok.RedeemedBy)
	}
	hasAdmin, err := s.AnyAdminExists(context.Background())
	if err != nil {
		t.Fatalf("AnyAdminExists: %v", err)
	}
	if hasAdmin {
		t.Fatal("expected no admin on a fresh DB")
	}
}

// TestBootstrapTokenNotReSeededOnRerun asserts that re-running
// the migration with an existing pending token does NOT mint a
// second token (the seed uses NOT EXISTS).
func TestBootstrapTokenNotReSeededOnRerun(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	first, _, err := s.LookupBootstrapToken(ctx)
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if err := migrate(ctx, s.pool); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	second, _, err := s.LookupBootstrapToken(ctx)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if first.Token != second.Token {
		t.Fatalf("token rotated across migrations: first=%s second=%s", first.Token, second.Token)
	}
}

// TestRedeemBootstrapToken_HappyPath redeems a valid token after
// a member has been auto-joined to the default org and asserts
// the membership role flips to admin.
func TestRedeemBootstrapToken_HappyPath(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	const fp = "deadbeefcafebabe"
	if err := s.EnsureDefaultMembership(ctx, fp); err != nil {
		t.Fatalf("EnsureDefaultMembership: %v", err)
	}

	tok, _, err := s.LookupBootstrapToken(ctx)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := s.RedeemBootstrapToken(ctx, tok.Token, fp); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// Token row should be marked redeemed.
	post, _, err := s.LookupBootstrapToken(ctx)
	if err != nil {
		t.Fatalf("Lookup after: %v", err)
	}
	if post.Pending() {
		t.Fatal("token still pending after redeem")
	}
	if post.RedeemedBy != fp {
		t.Errorf("redeemed_by: %q want %q", post.RedeemedBy, fp)
	}

	// Membership role upgraded.
	mems, err := s.ListMemberships(ctx, fp)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(mems) != 1 || mems[0].Role != "admin" {
		t.Fatalf("memberships post-redeem: %+v", mems)
	}

	// AnyAdminExists now true.
	hasAdmin, err := s.AnyAdminExists(ctx)
	if err != nil {
		t.Fatalf("AnyAdminExists: %v", err)
	}
	if !hasAdmin {
		t.Fatal("expected admin to exist after redeem")
	}
}

// TestRedeemBootstrapToken_WrongTokenIsInvalid asserts a bad
// token returns ErrBootstrapTokenInvalid (no leak between
// "wrong token" vs "already redeemed").
func TestRedeemBootstrapToken_WrongTokenIsInvalid(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	const fp = "0011223344556677"
	if err := s.EnsureDefaultMembership(ctx, fp); err != nil {
		t.Fatalf("EnsureDefaultMembership: %v", err)
	}
	if err := s.RedeemBootstrapToken(ctx, "not-the-token", fp); !errors.Is(err, ErrBootstrapTokenInvalid) {
		t.Fatalf("got %v want ErrBootstrapTokenInvalid", err)
	}
}

// TestRedeemBootstrapToken_DoubleRedeemFails asserts the second
// redeem of a previously-redeemed token returns
// ErrBootstrapTokenInvalid.
func TestRedeemBootstrapToken_DoubleRedeemFails(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	const fp = "feedfacefeedface"
	if err := s.EnsureDefaultMembership(ctx, fp); err != nil {
		t.Fatalf("EnsureDefaultMembership: %v", err)
	}
	tok, _, _ := s.LookupBootstrapToken(ctx)
	if err := s.RedeemBootstrapToken(ctx, tok.Token, fp); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := s.RedeemBootstrapToken(ctx, tok.Token, fp); !errors.Is(err, ErrBootstrapTokenInvalid) {
		t.Fatalf("second redeem: got %v want ErrBootstrapTokenInvalid", err)
	}
}

// TestRedeemBootstrapToken_NonMemberReturnsNotMember asserts a
// caller who never auth-verified (and so is not in the default
// org) gets ErrBootstrapNotMember and the token stays pending.
func TestRedeemBootstrapToken_NonMemberReturnsNotMember(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	tok, _, _ := s.LookupBootstrapToken(ctx)
	if err := s.RedeemBootstrapToken(ctx, tok.Token, "stranger-fingerprint"); !errors.Is(err, ErrBootstrapNotMember) {
		t.Fatalf("got %v want ErrBootstrapNotMember", err)
	}
	// Token must still be pending — the redeem rolled back.
	post, _, _ := s.LookupBootstrapToken(ctx)
	if !post.Pending() {
		t.Fatal("token was consumed despite NotMember failure")
	}
}

// TestHandleAdminBootstrap_HappyPath runs the full HTTP redeem
// flow against a Postgres-backed Server: challenge → verify
// (auto-joins default org) → POST /admin/bootstrap with the
// redeemed token in a Bearer-authorized request.
func TestHandleAdminBootstrap_HappyPath(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ks := NewKeystore()
	fp, err := ks.Add("admin-claimer", pub)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	srv, err := New(Options{Store: s, Keystore: ks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	bearer := authedBearer(t, hs, fp, priv)

	tok, _, err := s.LookupBootstrapToken(context.Background())
	if err != nil {
		t.Fatalf("LookupBootstrapToken: %v", err)
	}

	resp, body := postWithBearer(t, hs.URL+adminBootstrapPath, bearer, proto.BootstrapRequest{Token: tok.Token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var br proto.BootstrapResponse
	if err := json.Unmarshal(body, &br); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if br.OrgName != "default" {
		t.Errorf("OrgName=%q want %q", br.OrgName, "default")
	}
	if br.OrgID == "" {
		t.Error("OrgID empty")
	}
	if br.Fingerprint != fp.String() {
		t.Errorf("Fingerprint=%q want %q", br.Fingerprint, fp.String())
	}

	// Membership upgraded to admin.
	mems, err := s.ListMemberships(context.Background(), fp.String())
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(mems) != 1 || mems[0].Role != "admin" {
		t.Fatalf("memberships: %+v", mems)
	}
}

// TestHandleAdminBootstrap_RejectsMissingAuth asserts the
// handler 401s when the request has no bearer token (BOOT.2:
// even bootstrap requires the standard auth handshake first).
func TestHandleAdminBootstrap_RejectsMissingAuth(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ks := NewKeystore()
	if _, err := ks.Add("k", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	srv, _ := New(Options{Store: s, Keystore: ks})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	resp, body := postJSON(t, hs.URL+adminBootstrapPath, proto.BootstrapRequest{Token: "anything"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

// TestHandleAdminBootstrap_RejectsBadToken asserts an
// authenticated caller with a wrong token gets 401 with the
// bootstrap_invalid_token code.
func TestHandleAdminBootstrap_RejectsBadToken(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ks := NewKeystore()
	fp, _ := ks.Add("k", pub)
	srv, _ := New(Options{Store: s, Keystore: ks})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	bearer := authedBearer(t, hs, fp, priv)
	resp, body := postWithBearer(t, hs.URL+adminBootstrapPath, bearer, proto.BootstrapRequest{Token: "no-such-token"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "bootstrap_invalid_token") {
		t.Errorf("expected error code in body: %s", body)
	}
}

// TestAnnounceBootstrap_WritesTokenFile asserts the startup
// announce call drops the token to disk when no admin exists.
func TestAnnounceBootstrap_WritesTokenFile(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap.token")

	rec := &recordingLogger{}
	announceBootstrapToken(context.Background(), s, path, rec)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	tok, _, _ := s.LookupBootstrapToken(context.Background())
	if !strings.Contains(string(got), tok.Token) {
		t.Fatalf("file contents %q do not contain token %q", got, tok.Token)
	}
	if !rec.warnedAboutToken() {
		t.Error("expected a WARN log including the bootstrap token")
	}
}

// TestAnnounceBootstrap_RemovesTokenFileWhenAdminExists asserts
// the startup announce cleans up the token file once an admin
// has been redeemed (idempotent: missing file is fine too).
func TestAnnounceBootstrap_RemovesTokenFileWhenAdminExists(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	const fp = "1122334455667788"
	if err := s.EnsureDefaultMembership(ctx, fp); err != nil {
		t.Fatalf("EnsureDefaultMembership: %v", err)
	}
	tok, _, _ := s.LookupBootstrapToken(ctx)
	if err := s.RedeemBootstrapToken(ctx, tok.Token, fp); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap.token")
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}

	announceBootstrapToken(ctx, s, path, &recordingLogger{})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected stale token file to be removed; stat err=%v", err)
	}
}

// authedBearer drives the standard challenge/verify flow against
// a Postgres-backed Server and returns the issued bearer token.
func authedBearer(t *testing.T, hs *httptest.Server, fp identity.Fingerprint, priv ed25519.PrivateKey) string {
	t.Helper()
	chResp, body := postJSON(t, hs.URL+authChallengePath, struct{}{})
	if chResp.StatusCode != http.StatusOK {
		t.Fatalf("challenge: %d %s", chResp.StatusCode, body)
	}
	var ch proto.AuthChallengeResponse
	if err := json.Unmarshal(body, &ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    ch.Nonce,
		Hostname: ch.Hostname,
		Scope:    "sync",
	})
	verifyResp, vbody := postJSON(t, hs.URL+authVerifyPath, proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(ed25519.Sign(priv, canonical)),
	})
	if verifyResp.StatusCode != http.StatusOK {
		t.Fatalf("verify: %d %s", verifyResp.StatusCode, vbody)
	}
	var v proto.AuthVerifyResponse
	if err := json.Unmarshal(vbody, &v); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	if v.AccessToken == "" {
		t.Fatal("missing access_token")
	}
	return v.AccessToken
}

// postWithBearer wraps postJSON to add an Authorization: Bearer
// header. Inlined rather than added to auth_test helpers to keep
// the bootstrap tests self-contained.
func postWithBearer(t *testing.T, url, bearer string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return resp, out
}

// recordingLogger captures structured log calls for the
// announceBootstrapToken tests.
type recordingLogger struct {
	warns []logEntry
}

type logEntry struct {
	msg  string
	args []any
}

func (r *recordingLogger) Warn(msg string, args ...any) {
	r.warns = append(r.warns, logEntry{msg: msg, args: append([]any(nil), args...)})
}

func (r *recordingLogger) warnedAboutToken() bool {
	for _, w := range r.warns {
		if !strings.Contains(w.msg, "claim this central node") {
			continue
		}
		for i := 0; i+1 < len(w.args); i += 2 {
			if k, _ := w.args[i].(string); k == "token" {
				if v, _ := w.args[i+1].(string); v != "" {
					return true
				}
			}
		}
	}
	return false
}
