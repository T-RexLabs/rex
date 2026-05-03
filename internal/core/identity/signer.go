package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
)

// Signer is the abstract signing operation
// (identity-and-trust.KEY.2). The Sign method takes a context so
// future HSM- or passkey-backed implementations can honour
// cancellation; the file-on-disk implementation in this package
// ignores it.
type Signer interface {
	Sign(ctx context.Context, payload []byte) ([]byte, error)
	PublicKey() ed25519.PublicKey
	Fingerprint() Fingerprint
	Handle() Handle
	Actor() Actor
}

// MemorySigner is the in-process Signer implementation. The
// FileSigner in store.go wraps this once it has loaded the key off
// disk.
type MemorySigner struct {
	keypair Keypair
}

// NewMemorySigner builds a Signer over an in-memory Keypair.
func NewMemorySigner(k Keypair) (*MemorySigner, error) {
	if k.Private == nil {
		return nil, errors.New("identity: NewMemorySigner needs a private key")
	}
	if !IsValidHandle(string(k.Handle)) {
		return nil, errors.New("identity: NewMemorySigner needs a valid handle")
	}
	return &MemorySigner{keypair: k}, nil
}

// Sign produces an ed25519 signature over payload.
func (s *MemorySigner) Sign(_ context.Context, payload []byte) ([]byte, error) {
	if s == nil || s.keypair.Private == nil {
		return nil, errors.New("identity: signer has no key")
	}
	return ed25519.Sign(s.keypair.Private, payload), nil
}

// PublicKey returns the verifying half.
func (s *MemorySigner) PublicKey() ed25519.PublicKey { return s.keypair.Public }

// Fingerprint returns the canonical short identity reference.
func (s *MemorySigner) Fingerprint() Fingerprint { return s.keypair.Fingerprint() }

// Handle returns the human-readable name.
func (s *MemorySigner) Handle() Handle { return s.keypair.Handle }

// Actor returns the local-role actor.
func (s *MemorySigner) Actor() Actor { return s.keypair.LocalActor() }

// Verify is a thin wrapper over ed25519.Verify so callers don't have
// to import crypto/ed25519 just to check a signature.
func Verify(pub ed25519.PublicKey, payload, signature []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, payload, signature)
}
