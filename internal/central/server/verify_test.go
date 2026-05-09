package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

func newSignedTestServer(t *testing.T, keyHandles ...string) (*Server, *httptest.Server, map[string]ed25519.PrivateKey) {
	return newSignedTestServerWithAudit(t, nil, keyHandles...)
}

// newSignedTestServerWithAudit is the audit-aware sibling. Wires
// the supplied AuthAuditAppender so token-lifecycle tests can
// assert which events fired.
func newSignedTestServerWithAudit(t *testing.T, audit AuthAuditAppender, keyHandles ...string) (*Server, *httptest.Server, map[string]ed25519.PrivateKey) {
	t.Helper()
	ks := NewKeystore()
	privs := make(map[string]ed25519.PrivateKey, len(keyHandles))
	for _, h := range keyHandles {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		if _, err := ks.Add(h, pub); err != nil {
			t.Fatalf("Add: %v", err)
		}
		privs[h] = priv
	}
	srv, err := New(Options{Keystore: ks, AuthAudit: audit})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs, privs
}

func mkSignedRecord(t *testing.T, id string, priv ed25519.PrivateKey) eventlog.Record {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)
	rec := eventlog.Record{
		ID:          id,
		Type:        "test.event",
		Version:     1,
		Actor:       (identity.Actor{Role: identity.RoleLocal, Fingerprint: fp}).String(),
		WorkspaceID: "ws-1",
		Payload:     json.RawMessage(`{}`),
	}
	canonical, err := eventlog.SigningBytes(rec)
	if err != nil {
		t.Fatalf("SigningBytes: %v", err)
	}
	rec.Signature = hex.EncodeToString(ed25519.Sign(priv, canonical))
	return rec
}

func postPush(t *testing.T, hs *httptest.Server, body proto.PushRequest, opts ...pushOpt) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, hs.URL+"/sync/events", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, opt := range opts {
		opt(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, respBody
}

type pushOpt func(*http.Request)

func withBearer(token string) pushOpt {
	return func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// issueTestToken bypasses the HTTP handshake and mints a token
// directly via the auth state. Lets signature-verification tests
// run without re-implementing the challenge-response dance.
func issueTestToken(t *testing.T, srv *Server, priv ed25519.PrivateKey) string {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	fp, err := identity.FingerprintOf(pub)
	if err != nil {
		t.Fatalf("FingerprintOf: %v", err)
	}
	tok, err := srv.auth.issueToken(fp, "sync")
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	return tok.legacyValue
}

func TestPushAcceptsSignedRecord(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	rec := mkSignedRecord(t, "ev-1", privs["alice"])
	token := issueTestToken(t, srv, privs["alice"])

	resp, body := postPush(t, hs, proto.PushRequest{Since: "", Events: []eventlog.Record{rec}}, withBearer(token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestPushRejectsUnregisteredFingerprint(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	// bob's keypair is not registered.
	_, bobPriv, _ := ed25519.GenerateKey(rand.Reader)
	rec := mkSignedRecord(t, "ev-1", bobPriv)
	// Use alice's token (registered) so the gate lets us through;
	// the per-event signature check is what we want to test.
	token := issueTestToken(t, srv, privs["alice"])

	resp, body := postPush(t, hs, proto.PushRequest{Since: "", Events: []eventlog.Record{rec}}, withBearer(token))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestPushRejectsTamperedRecord(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	rec := mkSignedRecord(t, "ev-1", privs["alice"])
	// tamper after signing
	rec.Payload = json.RawMessage(`{"changed":true}`)
	token := issueTestToken(t, srv, privs["alice"])

	resp, body := postPush(t, hs, proto.PushRequest{Since: "", Events: []eventlog.Record{rec}}, withBearer(token))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestPushRejectsRecordWithoutSignature(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	rec := mkSignedRecord(t, "ev-1", privs["alice"])
	rec.Signature = ""
	token := issueTestToken(t, srv, privs["alice"])

	resp, body := postPush(t, hs, proto.PushRequest{Since: "", Events: []eventlog.Record{rec}}, withBearer(token))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestPushSkipsVerificationWhenKeystoreEmpty(t *testing.T) {
	t.Parallel()

	srv, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// Send an unsigned record; should still be accepted because no
	// keys are registered (legacy/dev mode).
	rec := eventlog.Record{
		ID: "ev-1", Type: "test.event", Version: 1,
		Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws", Payload: json.RawMessage(`{}`),
	}
	resp, body := postPush(t, hs, proto.PushRequest{Since: "", Events: []eventlog.Record{rec}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestPushOneBadRecordRejectsWholeBatch(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	good := mkSignedRecord(t, "ev-1", privs["alice"])
	bad := mkSignedRecord(t, "ev-2", privs["alice"])
	bad.Signature = "deadbeef"
	token := issueTestToken(t, srv, privs["alice"])

	resp, body := postPush(t, hs, proto.PushRequest{
		Since:  "",
		Events: []eventlog.Record{good, bad},
	}, withBearer(token))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	// Atomicity: nothing was accepted (same server we authorized
	// against).
	n, err := srv.Store().Len(context.Background())
	if err != nil {
		t.Fatalf("Store().Len: %v", err)
	}
	if n != 0 {
		t.Fatalf("partial batch leaked through: len=%d", n)
	}
}
