package identity

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultStoreSignerCreates(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "identity")
	store := NewStore(dir)

	signer, err := EnsureDefaultStoreSigner(store)
	if err != nil {
		t.Fatalf("EnsureDefaultStoreSigner: %v", err)
	}
	if signer.Handle() != DefaultHandle {
		t.Fatalf("handle: got %q want %q", signer.Handle(), DefaultHandle)
	}
	if !signer.Actor().IsLocal() {
		t.Fatal("default signer should be a local actor")
	}

	// Second call returns the same persisted material — public
	// keys must match exactly (otherwise we'd re-key on every run).
	signer2, err := EnsureDefaultStoreSigner(store)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if signer2.Fingerprint() != signer.Fingerprint() {
		t.Fatalf("fingerprint drifted: %v vs %v", signer.Fingerprint(), signer2.Fingerprint())
	}
}

func TestEnsureDefaultStoreSignerRejectsNil(t *testing.T) {
	t.Parallel()

	if _, err := EnsureDefaultStoreSigner(nil); err == nil {
		t.Fatal("nil store should error")
	}
}

func TestSignFuncAdaptsSigner(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "identity")
	signer, err := EnsureDefaultStoreSigner(NewStore(dir))
	if err != nil {
		t.Fatalf("EnsureDefaultStoreSigner: %v", err)
	}
	fn := SignFunc(signer)
	sig, err := fn([]byte("hi"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !Verify(signer.PublicKey(), []byte("hi"), sig) {
		t.Fatal("adapted SignFunc signature failed to verify")
	}

	// Direct Sign call returns the same signature on the same
	// payload (ed25519 is deterministic), confirming the adapter
	// doesn't add any salt.
	direct, err := signer.Sign(context.Background(), []byte("hi"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if string(direct) != string(sig) {
		t.Fatal("SignFunc and direct Sign disagree")
	}
}

func TestSignFuncNilSignerReturnsNil(t *testing.T) {
	t.Parallel()

	if SignFunc(nil) != nil {
		t.Fatal("SignFunc(nil) should be nil")
	}
}
