package identity

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
)

// deterministicReader returns a reproducible byte stream for tests so
// keypair generation produces the same output every run
// (overview.ENG.4).
type deterministicReader struct {
	seed byte
}

func (d *deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = d.seed
		d.seed++
	}
	return len(p), nil
}

func TestGenerateKeypairIsDeterministicWithSeed(t *testing.T) {
	t.Parallel()

	a, err := GenerateKeypair("alice", &deterministicReader{seed: 42})
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	b, err := GenerateKeypair("alice", &deterministicReader{seed: 42})
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if !bytes.Equal(a.Public, b.Public) {
		t.Fatal("seeded generation produced different public keys")
	}
	if !bytes.Equal(a.Private, b.Private) {
		t.Fatal("seeded generation produced different private keys")
	}
}

func TestGenerateKeypairRejectsBadHandle(t *testing.T) {
	t.Parallel()

	if _, err := GenerateKeypair("", nil); err == nil {
		t.Fatal("empty handle should error")
	}
	if _, err := GenerateKeypair("BAD", nil); err == nil {
		t.Fatal("uppercase handle should error")
	}
}

func TestPEMRoundTripPrivate(t *testing.T) {
	t.Parallel()

	k, err := GenerateKeypair("alice", &deterministicReader{seed: 1})
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	body, err := MarshalPrivatePEM(k)
	if err != nil {
		t.Fatalf("MarshalPrivatePEM: %v", err)
	}
	if !strings.Contains(string(body), "PRIVATE KEY") {
		t.Fatalf("PEM block type missing: %s", body)
	}
	priv, err := ParsePrivatePEM(body)
	if err != nil {
		t.Fatalf("ParsePrivatePEM: %v", err)
	}
	if !bytes.Equal(priv, k.Private) {
		t.Fatal("round-tripped private key differs")
	}
}

func TestPEMRoundTripPublic(t *testing.T) {
	t.Parallel()

	k, err := GenerateKeypair("alice", &deterministicReader{seed: 2})
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	body, err := MarshalPublicPEM(k)
	if err != nil {
		t.Fatalf("MarshalPublicPEM: %v", err)
	}
	pub, err := ParsePublicPEM(body)
	if err != nil {
		t.Fatalf("ParsePublicPEM: %v", err)
	}
	if !bytes.Equal(pub, k.Public) {
		t.Fatal("round-tripped public key differs")
	}
}

func TestParsePEMRejectsBadInput(t *testing.T) {
	t.Parallel()

	if _, err := ParsePrivatePEM(nil); err == nil {
		t.Fatal("nil should error")
	}
	if _, err := ParsePublicPEM([]byte("not pem")); err == nil {
		t.Fatal("non-PEM should error")
	}
	// Type mismatch: feeding a public PEM to ParsePrivatePEM.
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 3})
	pubPEM, _ := MarshalPublicPEM(k)
	if _, err := ParsePrivatePEM(pubPEM); err == nil {
		t.Fatal("public PEM read as private should error")
	}
}

func TestKeypairFingerprintMatchesDirectComputation(t *testing.T) {
	t.Parallel()

	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 7})
	want, _ := FingerprintOf(k.Public)
	if k.Fingerprint() != want {
		t.Fatalf("fingerprint mismatch: %v vs %v", k.Fingerprint(), want)
	}
}

func TestKeypairLocalActorBuildsLocalRole(t *testing.T) {
	t.Parallel()

	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 7})
	a := k.LocalActor()
	if a.Role != RoleLocal {
		t.Fatalf("role: got %q want %q", a.Role, RoleLocal)
	}
	if a.Fingerprint != k.Fingerprint() {
		t.Fatal("actor fingerprint must equal keypair fingerprint")
	}
}

func TestKeypairSizes(t *testing.T) {
	t.Parallel()

	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 9})
	if len(k.Public) != ed25519.PublicKeySize {
		t.Fatalf("public size: got %d want %d", len(k.Public), ed25519.PublicKeySize)
	}
	if len(k.Private) != ed25519.PrivateKeySize {
		t.Fatalf("private size: got %d want %d", len(k.Private), ed25519.PrivateKeySize)
	}
}
