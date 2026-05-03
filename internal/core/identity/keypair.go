package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// PEM block types Rex emits and accepts.
const (
	pemTypePrivate = "PRIVATE KEY"
	pemTypePublic  = "PUBLIC KEY"
)

// Keypair is one ed25519 keypair plus the handle bound to it. The
// pair is in-memory only; persisted form lives in the filesystem
// behind the Store / Load helpers.
type Keypair struct {
	Handle  Handle
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// GenerateKeypair returns a fresh keypair for handle. The randomness
// source is injectable so tests are deterministic
// (overview.ENG.4).
func GenerateKeypair(handle Handle, randSource io.Reader) (Keypair, error) {
	if !IsValidHandle(string(handle)) {
		return Keypair{}, fmt.Errorf("identity: handle %q is not kebab-case", handle)
	}
	if randSource == nil {
		randSource = rand.Reader
	}
	pub, priv, err := ed25519.GenerateKey(randSource)
	if err != nil {
		return Keypair{}, fmt.Errorf("identity: generate ed25519: %w", err)
	}
	return Keypair{Handle: handle, Public: pub, Private: priv}, nil
}

// Fingerprint returns the SHA-256 prefix of the public key.
func (k Keypair) Fingerprint() Fingerprint {
	fp, _ := FingerprintOf(k.Public)
	return fp
}

// LocalActor returns this keypair's local-role Actor.
func (k Keypair) LocalActor() Actor {
	return Actor{Role: RoleLocal, Fingerprint: k.Fingerprint()}
}

// MarshalPrivatePEM returns the PEM-encoded form of the private key.
// Uses PKCS#8 because crypto/x509.MarshalPKCS8PrivateKey accepts
// ed25519 directly and produces a format every Go tool reads.
func MarshalPrivatePEM(k Keypair) ([]byte, error) {
	if k.Private == nil {
		return nil, errors.New("identity: nil private key")
	}
	der, err := x509.MarshalPKCS8PrivateKey(k.Private)
	if err != nil {
		return nil, fmt.Errorf("identity: marshal private: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePrivate, Bytes: der}), nil
}

// MarshalPublicPEM returns the PEM-encoded form of the public key.
func MarshalPublicPEM(k Keypair) ([]byte, error) {
	if k.Public == nil {
		return nil, errors.New("identity: nil public key")
	}
	der, err := x509.MarshalPKIXPublicKey(k.Public)
	if err != nil {
		return nil, fmt.Errorf("identity: marshal public: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePublic, Bytes: der}), nil
}

// ParsePrivatePEM decodes a PEM-encoded ed25519 private key.
func ParsePrivatePEM(body []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("identity: no PEM block in private key file")
	}
	if block.Type != pemTypePrivate {
		return nil, fmt.Errorf("identity: expected %q PEM block, got %q", pemTypePrivate, block.Type)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("identity: parse private: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("identity: private key is %T, want ed25519.PrivateKey", key)
	}
	return priv, nil
}

// ParsePublicPEM decodes a PEM-encoded ed25519 public key.
func ParsePublicPEM(body []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("identity: no PEM block in public key file")
	}
	if block.Type != pemTypePublic {
		return nil, fmt.Errorf("identity: expected %q PEM block, got %q", pemTypePublic, block.Type)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("identity: parse public: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("identity: public key is %T, want ed25519.PublicKey", key)
	}
	return pub, nil
}
