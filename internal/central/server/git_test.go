package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// testWorkspaceID is the canonical workspace id every git test
// runs against. The handlers + store are workspace-scoped, but
// the suite exercises a single workspace by default — multi-
// workspace isolation is covered in TestGitStoreScopesByWorkspace.
const testWorkspaceID = "ws-1"

// signGitPush signs a (workspaceID, path, baseRevision, content)
// tuple under priv and returns a populated GitPushRequest.
// Mirrors the local sync client's signing path so tests stay
// representative of real traffic.
func signGitPush(t *testing.T, priv ed25519.PrivateKey, wsID, path, baseRev, content string) proto.GitPushRequest {
	t.Helper()
	canonical, err := proto.GitSigningBytes(wsID, path, baseRev, content)
	if err != nil {
		t.Fatalf("GitSigningBytes: %v", err)
	}
	sig := ed25519.Sign(priv, canonical)
	return proto.GitPushRequest{
		WorkspaceID:  wsID,
		Entity:       path,
		BaseRevision: baseRev,
		Content:      content,
		Signature:    hex.EncodeToString(sig),
	}
}

func postGitPush(t *testing.T, hs *httptest.Server, req proto.GitPushRequest, opts ...pushOpt) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(req)
	httpReq, err := http.NewRequest(http.MethodPost, hs.URL+"/sync/git", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for _, opt := range opts {
		opt(httpReq)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func getGitPull(t *testing.T, hs *httptest.Server, wsID, path string, opts ...pushOpt) (*http.Response, []byte) {
	t.Helper()
	httpReq, err := http.NewRequest(http.MethodGet, hs.URL+"/sync/git/ws/"+wsID+"/"+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for _, opt := range opts {
		opt(httpReq)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// TestGitPushHappyPathDevMode covers the round-trip with an empty
// keystore (sync.SEC.* off). Confirms the server stores the entity,
// returns the content-addressable revision, and the GET surface
// echoes the same record.
func TestGitPushHappyPathDevMode(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	content := "spec_version: 1\nmetadata: { id: x }\n"
	req := proto.GitPushRequest{
		WorkspaceID:  testWorkspaceID,
		Entity:       "specs/x.yaml",
		BaseRevision: "",
		Content:      content,
	}
	resp, body := postGitPush(t, hs, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push status: %d body: %s", resp.StatusCode, body)
	}
	var pushRes proto.GitPushResponse
	if err := json.Unmarshal(body, &pushRes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pushRes.Entity != req.Entity {
		t.Fatalf("entity: got %q want %q", pushRes.Entity, req.Entity)
	}
	if pushRes.Revision != proto.GitContentRevision(content) {
		t.Fatalf("revision: got %q want sha256(content)", pushRes.Revision)
	}

	// GET it back.
	resp, body = getGitPull(t, hs, testWorkspaceID, "specs/x.yaml")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pull status: %d body: %s", resp.StatusCode, body)
	}
	var pullRes proto.GitPullResponse
	if err := json.Unmarshal(body, &pullRes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pullRes.Entity.Content != content {
		t.Fatalf("content: got %q want %q", pullRes.Entity.Content, content)
	}
	if pullRes.Entity.Revision != pushRes.Revision {
		t.Fatalf("revision mismatch: pull=%q push=%q", pullRes.Entity.Revision, pushRes.Revision)
	}
	// Sanity: the store now lists the path.
	paths, _ := srv.GitStore().List(context.Background(), testWorkspaceID)
	if len(paths) != 1 || paths[0] != "specs/x.yaml" {
		t.Fatalf("List: got %v want [specs/x.yaml]", paths)
	}
}

func TestGitPullUnknownEntityIs404(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	resp, body := getGitPull(t, hs, testWorkspaceID, "specs/never-pushed.yaml")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	var er proto.ErrorResponse
	_ = json.Unmarshal(body, &er)
	if er.Code != proto.ErrCodeGitUnknownEntity {
		t.Fatalf("code: got %q want %q", er.Code, proto.ErrCodeGitUnknownEntity)
	}
}

// TestGitPushConflictReturnsCurrent confirms a base_revision mismatch
// surfaces as 409 with the server's current revision in the body
// (sync.API.4 + sync.GIT.2).
func TestGitPushConflictReturnsCurrent(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	// First push lands.
	first := proto.GitPushRequest{WorkspaceID: testWorkspaceID, Entity: "workspace.yaml", Content: "name: alpha\n"}
	resp, body := postGitPush(t, hs, first)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first push: %d %s", resp.StatusCode, body)
	}
	var firstRes proto.GitPushResponse
	_ = json.Unmarshal(body, &firstRes)

	// Second push with a stale base_revision diverges.
	conflict := proto.GitPushRequest{
		WorkspaceID:  testWorkspaceID,
		Entity:       "workspace.yaml",
		BaseRevision: "deadbeef-stale",
		Content:      "name: beta\n",
	}
	resp, body = postGitPush(t, hs, conflict)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status: %d body: %s", resp.StatusCode, body)
	}
	var con proto.GitConflictResponse
	if err := json.Unmarshal(body, &con); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if con.ServerRevision != firstRes.Revision {
		t.Fatalf("server_revision: got %q want %q", con.ServerRevision, firstRes.Revision)
	}
	if con.ServerContent != "name: alpha\n" {
		t.Fatalf("server_content: %q", con.ServerContent)
	}
}

// TestGitPushIdempotentSameContent: pushing the same content again
// with the matching base_revision is the no-op path the client uses
// after a flaky network. Server returns 200 with the same revision.
func TestGitPushIdempotentSameContent(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	content := "name: alpha\n"
	first := proto.GitPushRequest{WorkspaceID: testWorkspaceID, Entity: "workspace.yaml", Content: content}
	resp, body := postGitPush(t, hs, first)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first: %d %s", resp.StatusCode, body)
	}
	var firstRes proto.GitPushResponse
	_ = json.Unmarshal(body, &firstRes)

	// Retry with the now-known revision as the base.
	retry := proto.GitPushRequest{
		WorkspaceID:  testWorkspaceID,
		Entity:       "workspace.yaml",
		BaseRevision: firstRes.Revision,
		Content:      content,
	}
	resp, body = postGitPush(t, hs, retry)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry: %d %s", resp.StatusCode, body)
	}
	var retryRes proto.GitPushResponse
	_ = json.Unmarshal(body, &retryRes)
	if retryRes.Revision != firstRes.Revision {
		t.Fatalf("revision changed: first=%q retry=%q", firstRes.Revision, retryRes.Revision)
	}
}

// TestGitRejectsNonGitMergedPath confirms sync.CAT.5 enforcement:
// pushing or pulling an event_sourced or derived path returns 400
// with the wrong-category code.
func TestGitRejectsNonGitMergedPath(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	for _, p := range []string{
		"events.log",                     // event_sourced
		"transcripts/r-1/0001.json",      // event_sourced
		"index.sqlite",                   // derived
		"snapshots/snap-x/manifest.json", // derived
		"drafts/origin.toml",             // derived
		"random.txt",                     // unregistered
	} {
		req := proto.GitPushRequest{WorkspaceID: testWorkspaceID, Entity: p, Content: "x"}
		resp, body := postGitPush(t, hs, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("push %q status: %d body: %s", p, resp.StatusCode, body)
		}
		var er proto.ErrorResponse
		_ = json.Unmarshal(body, &er)
		if er.Code != proto.ErrCodeWrongCategory {
			t.Fatalf("push %q code: got %q want %q", p, er.Code, proto.ErrCodeWrongCategory)
		}

		// And the GET surface refuses the same paths.
		resp, body = getGitPull(t, hs, testWorkspaceID, p)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("pull %q status: %d body: %s", p, resp.StatusCode, body)
		}
	}
}

func TestGitRejectsPathTraversal(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	req := proto.GitPushRequest{WorkspaceID: testWorkspaceID, Entity: "specs/../../../etc/passwd", Content: "x"}
	resp, body := postGitPush(t, hs, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

// TestGitPushRequiresAuthWhenKeystoreSet confirms sync.API.5: every
// /sync/git request requires a Bearer token when the keystore is
// configured.
func TestGitPushRequiresAuthWhenKeystoreSet(t *testing.T) {
	t.Parallel()

	_, hs, _ := newSignedTestServer(t, "alice")
	req := proto.GitPushRequest{WorkspaceID: testWorkspaceID, Entity: "workspace.yaml", Content: "x"}
	resp, body := postGitPush(t, hs, req) // no Bearer
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

// TestGitPushAcceptsSignedRequest covers the production path: signed
// content under a registered identity lands successfully.
func TestGitPushAcceptsSignedRequest(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	priv := privs["alice"]
	pub := priv.Public().(ed25519.PublicKey)
	fp, _ := identity.FingerprintOf(pub)
	token := issueTestToken(t, srv, priv)

	req := signGitPush(t, priv, testWorkspaceID, "specs/sync.yaml", "", "spec_version: 1\n")
	resp, body := postGitPush(t, hs, req, withBearer(token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}

	// Stored record should attribute the local-prefixed actor.
	stored, err := srv.GitStore().Get(context.Background(), testWorkspaceID, "specs/sync.yaml")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantActor := (identity.Actor{Role: identity.RoleLocal, Fingerprint: fp}).String()
	if stored.Actor != wantActor {
		t.Fatalf("actor: got %q want %q", stored.Actor, wantActor)
	}
	if !strings.HasPrefix(stored.Actor, "l-") {
		t.Fatalf("actor missing local prefix: %q", stored.Actor)
	}
}

// TestGitPushRejectsTamperedContent confirms a signature bound to the
// canonical input fails when content changes after signing — same
// semantics as the events surface (sync.SEC.*).
func TestGitPushRejectsTamperedContent(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	priv := privs["alice"]
	token := issueTestToken(t, srv, priv)

	req := signGitPush(t, priv, testWorkspaceID, "specs/sync.yaml", "", "original\n")
	req.Content = "tampered\n" // change after signing

	resp, body := postGitPush(t, hs, req, withBearer(token))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestGitPushRejectsUnregisteredFingerprint(t *testing.T) {
	t.Parallel()

	srv, hs, privs := newSignedTestServer(t, "alice")
	// bob is not registered.
	_, bobPriv, _ := ed25519.GenerateKey(rand.Reader)
	token := issueTestToken(t, srv, privs["alice"])
	req := signGitPush(t, bobPriv, testWorkspaceID, "workspace.yaml", "", "x\n")

	resp, body := postGitPush(t, hs, req, withBearer(token))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

// TestMemoryGitStoreScopesByWorkspace is the central invariant
// after the multi-workspace refactor: entities pushed for one
// workspace are invisible to reads on another. Two workspaces
// can hold the same path independently.
func TestMemoryGitStoreScopesByWorkspace(t *testing.T) {
	t.Parallel()
	st := NewMemoryGitStore()

	a := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-a", Content: "alpha"}
	b := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-b", Content: "beta"}
	if err := st.Put(context.Background(), "ws-a", a, ""); err != nil {
		t.Fatalf("put a: %v", err)
	}
	if err := st.Put(context.Background(), "ws-b", b, ""); err != nil {
		t.Fatalf("put b: %v", err)
	}

	got, err := st.Get(context.Background(), "ws-a", "workspace.yaml")
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	if got.Content != "alpha" {
		t.Errorf("ws-a content: got %q want alpha (cross-workspace leak)", got.Content)
	}
	got, err = st.Get(context.Background(), "ws-b", "workspace.yaml")
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if got.Content != "beta" {
		t.Errorf("ws-b content: got %q want beta (cross-workspace leak)", got.Content)
	}

	pathsA, _ := st.List(context.Background(), "ws-a")
	pathsB, _ := st.List(context.Background(), "ws-b")
	if len(pathsA) != 1 || pathsA[0] != "workspace.yaml" {
		t.Errorf("ws-a list: %v", pathsA)
	}
	if len(pathsB) != 1 || pathsB[0] != "workspace.yaml" {
		t.Errorf("ws-b list: %v", pathsB)
	}

	// Unknown workspace yields not-found, not the entity from
	// some other workspace.
	if _, err := st.Get(context.Background(), "ws-ghost", "workspace.yaml"); err == nil {
		t.Error("ws-ghost get: expected unknown-entity error")
	}
	if rows, _ := st.List(context.Background(), "ws-ghost"); len(rows) != 0 {
		t.Errorf("ws-ghost list: got %v want empty", rows)
	}

	// ListWorkspaces enumerates both.
	ids := st.ListWorkspaces()
	if len(ids) != 2 || ids[0] != "ws-a" || ids[1] != "ws-b" {
		t.Errorf("ListWorkspaces: %v want [ws-a ws-b]", ids)
	}

	// Empty workspace id rejects on every operation.
	if _, err := st.Get(context.Background(), "", "x"); err == nil {
		t.Error("Get(\"\"): expected error")
	}
	if err := st.Put(context.Background(), "", a, ""); err == nil {
		t.Error("Put(\"\"): expected error")
	}
	if _, err := st.List(context.Background(), ""); err == nil {
		t.Error("List(\"\"): expected error")
	}
}

// TestGitPushRejectsMissingWorkspaceID confirms the handler
// surfaces a 400 when the wire body omits workspace_id.
func TestGitPushRejectsMissingWorkspaceID(t *testing.T) {
	t.Parallel()
	_, hs := newTestServer(t)
	req := proto.GitPushRequest{Entity: "workspace.yaml", Content: "x"}
	resp, body := postGitPush(t, hs, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
}

func TestMemoryGitStoreEmptyBaseAfterCreatedConflicts(t *testing.T) {
	t.Parallel()
	// Direct store-level test: if the entity already exists, an
	// empty base revision must not silently overwrite — that would
	// be the "client thinks it's a brand-new file" race.
	st := NewMemoryGitStore()
	rec := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-1", Content: "alpha"}
	if err := st.Put(context.Background(), testWorkspaceID, rec, ""); err != nil {
		t.Fatalf("first put: %v", err)
	}
	rec2 := proto.GitEntity{Path: "workspace.yaml", Revision: "rev-2", Content: "beta"}
	err := st.Put(context.Background(), testWorkspaceID, rec2, "")
	var conflict *GitRevisionConflictError
	if err == nil {
		t.Fatal("expected conflict; got nil")
	}
	if !asConflict(err, &conflict) {
		t.Fatalf("error type: %T %v", err, err)
	}
	if conflict.ServerCurrent.Revision != "rev-1" {
		t.Fatalf("server_current revision: got %q want rev-1", conflict.ServerCurrent.Revision)
	}
}

// asConflict is a tiny errors.As shim that keeps the conflict-assertion
// call sites compact.
func asConflict(err error, target **GitRevisionConflictError) bool {
	return errors.As(err, target)
}
