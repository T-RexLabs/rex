package identity

import (
	"context"
	"testing"
)

func TestMemorySignerSignVerifies(t *testing.T) {
	t.Parallel()

	k, err := GenerateKeypair("alice", &deterministicReader{seed: 11})
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	signer, err := NewMemorySigner(k)
	if err != nil {
		t.Fatalf("NewMemorySigner: %v", err)
	}

	payload := []byte("rex test payload")
	sig, err := signer.Sign(context.Background(), payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !Verify(signer.PublicKey(), payload, sig) {
		t.Fatal("signature failed to verify under signer's own public key")
	}
	if Verify(signer.PublicKey(), []byte("tampered"), sig) {
		t.Fatal("signature verified for tampered payload")
	}
}

func TestMemorySignerExposesIdentity(t *testing.T) {
	t.Parallel()

	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 12})
	signer, err := NewMemorySigner(k)
	if err != nil {
		t.Fatalf("NewMemorySigner: %v", err)
	}
	if signer.Handle() != "alice" {
		t.Fatalf("handle: got %q", signer.Handle())
	}
	if signer.Fingerprint() != k.Fingerprint() {
		t.Fatal("fingerprint should match keypair")
	}
	if !signer.Actor().IsLocal() {
		t.Fatal("signer should expose local actor")
	}
}

func TestNewMemorySignerRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	if _, err := NewMemorySigner(Keypair{}); err == nil {
		t.Fatal("empty keypair should error")
	}
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 13})
	bad := k
	bad.Handle = "BAD"
	if _, err := NewMemorySigner(bad); err == nil {
		t.Fatal("invalid handle should error")
	}
}

func TestVerifyHandlesShortKey(t *testing.T) {
	t.Parallel()

	if Verify(nil, nil, nil) {
		t.Fatal("nil inputs must not verify")
	}
	if Verify([]byte("short"), nil, nil) {
		t.Fatal("short key must not verify")
	}
}
