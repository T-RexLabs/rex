package sync

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asabla/rex/internal/central/server"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// startServerWithSigner mirrors the in-process central but with the
// given signer pre-registered in its keystore.
func startServerWithSigner(t *testing.T, signer identity.Signer) *httptest.Server {
	t.Helper()
	ks := server.NewKeystore()
	if _, err := ks.Add(string(signer.Handle()), signer.PublicKey()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	srv, err := server.New(server.Options{Keystore: ks})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs
}

// signedRecord builds a record signed by signer (covering the
// canonical SigningBytes input). Used to seed test events.log files.
func signedRecord(t *testing.T, id string, signer identity.Signer) eventlog.Record {
	t.Helper()
	rec := eventlog.Record{
		ID:          id,
		Type:        "test.event",
		Version:     1,
		Actor:       signer.Actor().String(),
		WorkspaceID: "ws-test",
		Payload:     json.RawMessage(`{}`),
	}
	canonical, err := eventlog.SigningBytes(rec)
	if err != nil {
		t.Fatalf("SigningBytes: %v", err)
	}
	sig, err := signer.Sign(context.Background(), canonical)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec.Signature = hex.EncodeToString(sig)
	return rec
}

func TestClientHandshakeAndPush(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "identity")
	store := identity.NewStore(dir)
	signer, err := identity.EnsureDefaultStoreSigner(store)
	if err != nil {
		t.Fatalf("EnsureDefaultStoreSigner: %v", err)
	}
	hs := startServerWithSigner(t, signer)
	c := NewClient(hs.URL).WithSigner(signer)

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")
	rec := signedRecord(t, "ev-1", signer)
	f, _ := openAppend(logPath)
	if err := appendRaw(f, rec); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	_ = f.Close()

	// First push triggers handshake on 401, then retries.
	res, err := c.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	})
	if err != nil {
		t.Fatalf("PushOnly: %v", err)
	}
	if res.Accepted != 1 || res.HeadID != "ev-1" {
		t.Fatalf("push result: %+v", res)
	}
}

func TestClientPushFailsWithoutSignerWhenServerRequiresAuth(t *testing.T) {
	t.Parallel()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	ks := server.NewKeystore()
	_, _ = ks.Add("alice", pub)
	srv, _ := server.New(server.Options{Keystore: ks})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	c := NewClient(hs.URL) // no WithSigner

	// Set up a workspace with one signed record so PushOnly has
	// something to send. The record is signed by a DIFFERENT key
	// (an unrelated one) — but the test point is that the client
	// has no signer to handshake with, so the request fails on
	// 401 without retry.
	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tmpStore := identity.NewStore(filepath.Join(t.TempDir(), "extra"))
	tmpSigner, err := identity.EnsureDefaultStoreSigner(tmpStore)
	if err != nil {
		t.Fatalf("EnsureDefaultStoreSigner: %v", err)
	}
	rec := signedRecord(t, "ev-1", tmpSigner)
	f, _ := openAppend(filepath.Join(wsRoot, ".rex", "events.log"))
	_ = appendRaw(f, rec)
	_ = f.Close()

	_, err = c.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary",
		EventsLogPath: filepath.Join(wsRoot, ".rex", "events.log"),
	})
	if err == nil {
		t.Fatal("expected error when server requires auth and client has no signer")
	}
}

func TestClientHandshakeReusedAcrossRequests(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "identity")
	store := identity.NewStore(dir)
	signer, err := identity.EnsureDefaultStoreSigner(store)
	if err != nil {
		t.Fatalf("EnsureDefaultStoreSigner: %v", err)
	}
	hs := startServerWithSigner(t, signer)
	c := NewClient(hs.URL).WithSigner(signer)

	wsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsRoot, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := filepath.Join(wsRoot, ".rex", "events.log")
	f, _ := openAppend(logPath)
	if err := appendRaw(f, signedRecord(t, "ev-1", signer)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = f.Close()

	// First push handshakes + pushes.
	if _, err := c.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	}); err != nil {
		t.Fatalf("first push: %v", err)
	}
	// Add another record and push again; the cached token should
	// be reused (no second handshake needed).
	f, _ = openAppend(logPath)
	if err := appendRaw(f, signedRecord(t, "ev-2", signer)); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	_ = f.Close()

	res, err := c.PushOnly(context.Background(), RunArgs{
		WorkspaceRoot: wsRoot, Remote: "primary", EventsLogPath: logPath,
	})
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if res.Accepted != 1 || res.HeadID != "ev-2" {
		t.Fatalf("second push result: %+v", res)
	}
}
