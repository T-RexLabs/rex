package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs
}

func clientRec(id string) eventlog.Record {
	return eventlog.Record{
		ID:          id,
		Type:        "test.event",
		Version:     1,
		Actor:       "l-aaaaaaaaaaaaaaaa",
		WorkspaceID: "ws-1",
		Payload:     json.RawMessage(`{}`),
	}
}

func TestServerStateExposesIdentity(t *testing.T) {
	t.Parallel()

	srv, hs := newTestServer(t)
	resp, err := http.Get(hs.URL + "/sync/state")
	if err != nil {
		t.Fatalf("GET /sync/state: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	var state proto.StateResponse
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.ProtocolVersion != proto.ProtocolVersion {
		t.Fatalf("proto version: got %d", state.ProtocolVersion)
	}
	if state.Fingerprint != srv.keypair.Fingerprint().String() {
		t.Fatalf("fingerprint mismatch: %q vs %q", state.Fingerprint, srv.keypair.Fingerprint().String())
	}
	if !strings.HasPrefix(state.Actor, "c-") {
		t.Fatalf("actor should start with c-: %q", state.Actor)
	}
	if state.HeadID != "" {
		t.Fatalf("empty server head: got %q", state.HeadID)
	}
}

func TestServerStateRejectsNonGet(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/sync/state", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func push(t *testing.T, hs *httptest.Server, since string, events []eventlog.Record) (*http.Response, []byte) {
	t.Helper()
	body, err := json.Marshal(proto.PushRequest{Since: since, Events: events})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(hs.URL+"/sync/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, respBody
}

func TestServerPushHappyPath(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	resp, body := push(t, hs, "", []eventlog.Record{clientRec("e1"), clientRec("e2")})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	var pr proto.PushResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pr.HeadID != "e2" {
		t.Fatalf("head: got %q", pr.HeadID)
	}
	if pr.Accepted != 2 || pr.Duplicates != 0 {
		t.Fatalf("counts: %+v", pr)
	}
}

func TestServerPushIdempotent(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	if resp, body := push(t, hs, "", []eventlog.Record{clientRec("a")}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first push: %d %s", resp.StatusCode, body)
	}
	resp, body := push(t, hs, "a", []eventlog.Record{clientRec("a"), clientRec("b")})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second push: %d %s", resp.StatusCode, body)
	}
	var pr proto.PushResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pr.HeadID != "b" {
		t.Fatalf("head: %q", pr.HeadID)
	}
	if pr.Accepted != 1 || pr.Duplicates != 1 {
		t.Fatalf("counts: %+v", pr)
	}
}

func TestServerPushRejectsBadCursor(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	if resp, _ := push(t, hs, "", []eventlog.Record{clientRec("a")}); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed push failed")
	}
	resp, body := push(t, hs, "ghost", []eventlog.Record{clientRec("b")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", resp.StatusCode, body)
	}
	var conflict proto.ConflictResponse
	if err := json.Unmarshal(body, &conflict); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conflict.ServerHead != "a" {
		t.Fatalf("server head: got %q", conflict.ServerHead)
	}
}

func TestServerPushDivergenceReturnsTail(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	if resp, _ := push(t, hs, "", []eventlog.Record{clientRec("a"), clientRec("b")}); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed push failed")
	}
	// Client thinks the head is "a", but the server has "b" too.
	resp, body := push(t, hs, "a", []eventlog.Record{clientRec("c")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409: status=%d body=%s", resp.StatusCode, body)
	}
	var conflict proto.ConflictResponse
	if err := json.Unmarshal(body, &conflict); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conflict.ServerHead != "b" {
		t.Fatalf("server head: got %q want b", conflict.ServerHead)
	}
	if len(conflict.DivergingTail) != 1 || conflict.DivergingTail[0].ID != "b" {
		t.Fatalf("diverging tail: %+v", conflict.DivergingTail)
	}
}

func TestServerPushRejectsBadRecord(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	bad := clientRec("e1")
	bad.WorkspaceID = ""
	resp, body := push(t, hs, "", []eventlog.Record{bad})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400: %d %s", resp.StatusCode, body)
	}
}

func TestServerEventsSSE(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	if resp, _ := push(t, hs, "", []eventlog.Record{clientRec("e1"), clientRec("e2"), clientRec("e3")}); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed push failed")
	}
	resp, err := http.Get(hs.URL + "/sync/events?since=e1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type: %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if strings.Count(bodyStr, "data: ") != 2 {
		t.Fatalf("expected 2 data frames, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"id":"e2"`) || !strings.Contains(bodyStr, `"id":"e3"`) {
		t.Fatalf("frame contents: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, ": end") {
		t.Fatalf("missing end marker: %s", bodyStr)
	}
}

func TestServerEventsSSEEmptyStoreEmits(t *testing.T) {
	t.Parallel()

	_, hs := newTestServer(t)
	resp, err := http.Get(hs.URL + "/sync/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), ": end") {
		t.Fatalf("expected end marker even on empty store: %s", body)
	}
}

func TestServerActorIsCentral(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t)
	if !srv.Actor().IsCentral() {
		t.Fatalf("actor should be central: %s", srv.Actor())
	}
	if srv.Actor().Role != identity.RoleCentral {
		t.Fatalf("role: got %q", srv.Actor().Role)
	}
}

func TestServerAcceptsCustomKeypair(t *testing.T) {
	t.Parallel()

	kp, err := identity.GenerateKeypair("custom-central", nil)
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	srv, err := New(Options{Keypair: &kp})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.Actor().Fingerprint != kp.Fingerprint() {
		t.Fatal("custom keypair fingerprint not honoured")
	}
}
