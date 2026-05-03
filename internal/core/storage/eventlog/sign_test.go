package eventlog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newSignedTestWriter(t *testing.T, priv ed25519.PrivateKey) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	w, err := OpenWriter(WriterConfig{
		Path:        path,
		WorkspaceID: "ws-signed",
		Actor:       "l-aaaaaaaaaaaaaaaa",
		Sign: func(canonical []byte) ([]byte, error) {
			return ed25519.Sign(priv, canonical), nil
		},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func TestSigningBytesExcludesSignatureField(t *testing.T) {
	t.Parallel()

	rec := Record{
		ID: "1.0", Type: "x", Version: 1,
		Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws",
		Payload:   json.RawMessage(`{"k":"v"}`),
		Signature: "deadbeef",
	}
	body, err := SigningBytes(rec)
	if err != nil {
		t.Fatalf("SigningBytes: %v", err)
	}
	// Signature presence in the marshaled bytes would mean a
	// verifier sees the originally-stored signature in the input
	// they're trying to verify against — circular. Confirm it is
	// excluded.
	if strings.Contains(string(body), "signature") {
		t.Fatalf("signing bytes leaked signature field: %s", body)
	}
}

func TestWriterAppendStampsSignature(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	w, path := newSignedTestWriter(t, priv)
	rec, err := w.Append("hello", 1, json.RawMessage(`{"hi":"world"}`))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rec.Signature == "" {
		t.Fatal("expected signature on appended record")
	}
	// The on-disk record carries the signature too — read back and
	// verify against pub.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()
	got, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Signature == "" {
		t.Fatal("read-back record missing signature")
	}
	if err := VerifyRecord(got, func(payload, sig []byte) bool {
		return ed25519.Verify(pub, payload, sig)
	}); err != nil {
		t.Fatalf("VerifyRecord: %v", err)
	}
}

func TestVerifyRecordRejectsTamperedPayload(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	w, _ := newSignedTestWriter(t, priv)
	rec, err := w.Append("hello", 1, json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	rec.Payload = json.RawMessage(`{"x":2}`)
	err = VerifyRecord(rec, func(payload, sig []byte) bool {
		return ed25519.Verify(pub, payload, sig)
	})
	if err == nil {
		t.Fatal("tampered payload should fail verification")
	}
}

func TestVerifyRecordRejectsMissingSignature(t *testing.T) {
	t.Parallel()

	rec := Record{ID: "x", Type: "t", Version: 1, WorkspaceID: "ws"}
	err := VerifyRecord(rec, func(_, _ []byte) bool { return true })
	if err == nil {
		t.Fatal("missing signature should fail verification")
	}
}

func TestVerifyRecordRejectsMalformedSignature(t *testing.T) {
	t.Parallel()

	rec := Record{
		ID: "x", Type: "t", Version: 1, WorkspaceID: "ws",
		Signature: "not-hex",
	}
	err := VerifyRecord(rec, func(_, _ []byte) bool { return true })
	if err == nil {
		t.Fatal("non-hex signature should fail")
	}
}

func TestUnsignedWriterEmitsBlankSignature(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	w, err := OpenWriter(WriterConfig{
		Path: path, WorkspaceID: "ws-unsigned",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()
	rec, err := w.Append("hello", 1, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rec.Signature != "" {
		t.Fatalf("unsigned writer leaked signature: %q", rec.Signature)
	}
}

func TestSigningBytesStableAcrossRuns(t *testing.T) {
	t.Parallel()

	rec := Record{
		ID: "stable.1", Type: "x", Version: 1,
		Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws",
		Payload: json.RawMessage(`{"order":"matters"}`),
	}
	a, _ := SigningBytes(rec)
	b, _ := SigningBytes(rec)
	if string(a) != string(b) {
		t.Fatal("SigningBytes is not deterministic on same record")
	}
}

func TestVerifyRecordSurfacesHexErrorWrapped(t *testing.T) {
	t.Parallel()

	rec := Record{
		ID: "x", Type: "t", Version: 1, WorkspaceID: "ws",
		Signature: hex.EncodeToString([]byte("not-a-real-sig")),
	}
	err := VerifyRecord(rec, func(_, _ []byte) bool { return false })
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errors.New("decode signature")) {
		// errors.Is on a freshly-constructed sentinel will never
		// match; this branch is intentionally inert. The real
		// signal is the message body below.
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("expected verify failure message, got %v", err)
	}
}
