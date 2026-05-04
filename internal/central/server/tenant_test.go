package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// signedClient bundles the per-test fingerprint + signing key
// + verified bearer token for an authenticated central. Lets
// the per-test setup stay readable.
type signedClient struct {
	t   *testing.T
	hs  *httptest.Server
	fp  string
	key ed25519.PrivateKey
	tok string
}

func newAuthedClient(t *testing.T, srv *Server) *signedClient {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fp, err := srv.Keystore().Add("test-key-"+t.Name(), pub)
	if err != nil {
		t.Fatalf("Keystore.Add: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Challenge.
	chResp, err := http.Post(hs.URL+"/auth/challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /auth/challenge: %v", err)
	}
	defer chResp.Body.Close()
	var ch proto.AuthChallengeResponse
	if err := json.NewDecoder(chResp.Body).Decode(&ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    ch.Nonce,
		Hostname: ch.Hostname,
		Scope:    "sync",
	})
	verifyBody, _ := json.Marshal(proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Signature:   hex.EncodeToString(ed25519.Sign(priv, canonical)),
		Scope:       "sync",
	})
	vResp, err := http.Post(hs.URL+"/auth/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("POST /auth/verify: %v", err)
	}
	defer vResp.Body.Close()
	if vResp.StatusCode != http.StatusOK {
		t.Fatalf("verify status: %d", vResp.StatusCode)
	}
	var vBody proto.AuthVerifyResponse
	if err := json.NewDecoder(vResp.Body).Decode(&vBody); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	return &signedClient{t: t, hs: hs, fp: fp.String(), key: priv, tok: vBody.AccessToken}
}

func (c *signedClient) push(t *testing.T, since string, recs []eventlog.Record, headers ...[2]string) (*http.Response, []byte) {
	t.Helper()
	body, _ := json.Marshal(proto.PushRequest{Since: since, Events: recs})
	req, _ := http.NewRequest(http.MethodPost, c.hs.URL+"/sync/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.tok)
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp, out
}

// TestPushAcceptsAfterAutoJoinOnPostgresStore: the auth-verify
// hook auto-joins the verified fingerprint to the default org;
// a subsequent /sync/events POST resolves to that org and the
// PostgresStore's Append succeeds with the org id stamped.
func TestPushAcceptsAfterAutoJoinOnPostgresStore(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)

	srv, err := New(Options{Store: store, Keystore: NewKeystore()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := newAuthedClient(t, srv)

	rec := signedRecordForTest(t, c, "tenant-test-1", "ws-tenant-1")
	resp, body := c.push(t, "", []eventlog.Record{rec})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push: %d body=%s", resp.StatusCode, body)
	}
	// The event landed in the default org.
	ctx := defaultOrgCtx(t, store)
	n, err := store.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row in default org, got %d", n)
	}
	// And the workspaces row was bound to the default org.
	bound, exists, err := store.WorkspaceOrg(context.Background(), "ws-tenant-1")
	if err != nil {
		t.Fatalf("WorkspaceOrg: %v", err)
	}
	if !exists {
		t.Fatal("workspaces row missing")
	}
	defaultOrg, _ := store.LookupOrg(context.Background(), DefaultOrgName)
	if bound != defaultOrg.ID {
		t.Errorf("workspace bound to %q, want default org %q", bound, defaultOrg.ID)
	}
}

// TestPushRejectedWhenWorkspaceBoundToDifferentOrg: bind a
// workspace to org A directly, then have an identity in org B
// try to push for the same workspace_id. Server returns 403.
func TestPushRejectedWhenWorkspaceBoundToDifferentOrg(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)

	// Create a second org and a workspace bound to it directly,
	// bypassing the application-level binding (we want to set
	// up the cross-org state without going through Append).
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO orgs (name, display_name) VALUES ('other', 'Other org')`,
	); err != nil {
		t.Fatalf("seed other org: %v", err)
	}
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO workspaces (id, org_id, first_actor)
		 SELECT 'ws-other-org', id, '' FROM orgs WHERE name = 'other'`,
	); err != nil {
		t.Fatalf("seed workspace binding: %v", err)
	}

	srv, err := New(Options{Store: store, Keystore: NewKeystore()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := newAuthedClient(t, srv) // joined to default org

	rec := signedRecordForTest(t, c, "cross-org-1", "ws-other-org")
	resp, body := c.push(t, "", []eventlog.Record{rec})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d body=%s (expected 403)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("workspace_org_mismatch")) {
		t.Errorf("expected workspace_org_mismatch error, got: %s", body)
	}
	// And the event was NOT inserted.
	n, err := store.Len(defaultOrgCtx(t, store))
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 0 {
		t.Errorf("event leaked through: %d rows in default org", n)
	}
}

// TestPushAmbiguousOrgRejectedWhenMultiOrgIdentity: a multi-org
// identity that doesn't send X-Rex-Org gets a 400, per
// TENANT.1-note "the API never picks for the user".
func TestPushAmbiguousOrgRejectedWhenMultiOrgIdentity(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)

	srv, err := New(Options{Store: store, Keystore: NewKeystore()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := newAuthedClient(t, srv) // auto-joined to default org

	// Create a second org and add this identity to it too, so
	// we end up with 2 memberships → ambiguous without header.
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO orgs (name, display_name) VALUES ('other', 'Other')`,
	); err != nil {
		t.Fatalf("seed other org: %v", err)
	}
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO org_memberships (org_id, fingerprint, role)
		 SELECT id, $1, 'member' FROM orgs WHERE name = 'other'`,
		c.fp,
	); err != nil {
		t.Fatalf("add second membership: %v", err)
	}

	rec := signedRecordForTest(t, c, "ambig-1", "ws-ambig")
	resp, body := c.push(t, "", []eventlog.Record{rec})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s (expected 400)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("ambiguous_org")) {
		t.Errorf("expected ambiguous_org error, got: %s", body)
	}

	// Same identity with X-Rex-Org: default → success.
	resp2, body2 := c.push(t, "", []eventlog.Record{rec}, [2]string{XRexOrgHeader, "default"})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("with X-Rex-Org status: %d body=%s", resp2.StatusCode, body2)
	}
}

// signedRecordForTest builds a record signed with c.key so the
// central's per-event signature check (sync.SEC.1) passes.
func signedRecordForTest(t *testing.T, c *signedClient, id, workspaceID string) eventlog.Record {
	t.Helper()
	rec := eventlog.Record{
		ID: id, Type: "test.event", Version: 1,
		Actor:       "l-" + c.fp,
		WorkspaceID: workspaceID,
		Payload:     []byte(`{}`),
		Timestamp:   eventlog.HLC{Wall: 1700000000, Logical: 1},
	}
	canonical, err := eventlog.SigningBytes(rec)
	if err != nil {
		t.Fatalf("SigningBytes: %v", err)
	}
	rec.Signature = hex.EncodeToString(ed25519.Sign(c.key, canonical))
	return rec
}
