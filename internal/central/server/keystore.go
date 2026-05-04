package server

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/asabla/rex/internal/core/identity"
)

// AuthorizedKey is one entry in the server's authorized-keys file.
// The file format is TOML to match the rest of Rex's *.toml configs:
//
//	[[keys]]
//	handle = "alice"
//	fingerprint = "1234567890abcdef"
//	public_key_pem = """
//	-----BEGIN PUBLIC KEY-----
//	...
//	-----END PUBLIC KEY-----
//	"""
//
// The handle is informational; signature verification routes by
// fingerprint, which is derived from the public key (so the file
// stays consistent even if a typo creeps into the fingerprint
// field — the loader recomputes and compares).
type AuthorizedKey struct {
	Handle       string `toml:"handle"`
	Fingerprint  string `toml:"fingerprint"`
	PublicKeyPEM string `toml:"public_key_pem"`
	publicKey    ed25519.PublicKey
}

// authorizedKeysFile is the on-disk shape of `--keys <file>`.
type authorizedKeysFile struct {
	Keys []AuthorizedKey `toml:"keys"`
}

// Keystore is an in-memory map of fingerprint → public key the
// server consults when verifying signed records. Empty keystore
// means "no verification configured"; callers can branch on Empty()
// to short-circuit verification in dev mode.
type Keystore struct {
	mu   sync.RWMutex
	keys map[identity.Fingerprint]AuthorizedKey
}

// NewKeystore returns an empty keystore.
func NewKeystore() *Keystore {
	return &Keystore{keys: make(map[identity.Fingerprint]AuthorizedKey)}
}

// LoadKeystoreFile parses an authorized-keys TOML file and returns
// a populated Keystore. Missing file is an error (the operator asked
// for keys; if the file is absent we should not silently allow
// everything).
func LoadKeystoreFile(path string) (*Keystore, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("server: authorized-keys file %q does not exist", path)
		}
		return nil, fmt.Errorf("server: read keys file: %w", err)
	}
	var raw authorizedKeysFile
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("server: parse keys file: %w", err)
	}

	store := NewKeystore()
	for i, entry := range raw.Keys {
		if entry.PublicKeyPEM == "" {
			return nil, fmt.Errorf("server: keys[%d] missing public_key_pem", i)
		}
		pub, err := identity.ParsePublicPEM([]byte(entry.PublicKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("server: keys[%d] (handle=%q) parse pem: %w", i, entry.Handle, err)
		}
		fp, err := identity.FingerprintOf(pub)
		if err != nil {
			return nil, fmt.Errorf("server: keys[%d] fingerprint: %w", i, err)
		}
		// If the file declared a fingerprint, ensure it matches
		// what we derived. This catches copy-paste typos.
		if entry.Fingerprint != "" && entry.Fingerprint != fp.String() {
			return nil, fmt.Errorf(
				"server: keys[%d] (handle=%q) fingerprint mismatch: declared=%q derived=%q",
				i, entry.Handle, entry.Fingerprint, fp.String())
		}
		entry.Fingerprint = fp.String()
		entry.publicKey = pub
		if _, dup := store.keys[fp]; dup {
			return nil, fmt.Errorf("server: keys[%d] (handle=%q) duplicate fingerprint %s", i, entry.Handle, fp)
		}
		store.keys[fp] = entry
	}
	return store, nil
}

// Add inserts (or replaces) a key by fingerprint. Used by tests and
// future admin endpoints that mutate the keystore at runtime.
func (s *Keystore) Add(handle string, pub ed25519.PublicKey) (identity.Fingerprint, error) {
	if pub == nil {
		return identity.Fingerprint{}, errors.New("server: Add nil public key")
	}
	fp, err := identity.FingerprintOf(pub)
	if err != nil {
		return identity.Fingerprint{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[fp] = AuthorizedKey{
		Handle:      handle,
		Fingerprint: fp.String(),
		publicKey:   pub,
	}
	return fp, nil
}

// Lookup returns the AuthorizedKey for fp, plus a found flag.
func (s *Keystore) Lookup(fp identity.Fingerprint) (AuthorizedKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[fp]
	return k, ok
}

// Verify checks that signature is valid for canonical under the
// public key registered for fp. Returns nil on success, or an error
// describing why verification failed. fp is not present in the
// keystore is ErrUnknownIdentity; verification mismatch is
// ErrInvalidSignature.
func (s *Keystore) Verify(fp identity.Fingerprint, canonical, signature []byte) error {
	k, ok := s.Lookup(fp)
	if !ok {
		return fmt.Errorf("%w: fingerprint %s not registered", ErrUnknownIdentity, fp)
	}
	if !ed25519.Verify(k.publicKey, canonical, signature) {
		return ErrInvalidSignature
	}
	return nil
}

// Empty reports whether the keystore has any keys. Used to short-
// circuit verification in dev mode (no --keys flag).
func (s *Keystore) Empty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.keys) == 0
}

// Handles returns every registered handle in lex order. Useful for
// admin/debug surfaces.
func (s *Keystore) Handles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k.Handle)
	}
	sort.Strings(out)
	return out
}

// ErrUnknownIdentity is returned when the actor's fingerprint is not
// in the keystore.
var ErrUnknownIdentity = errors.New("server: unknown identity")

// ErrInvalidSignature is returned when a record's signature does
// not verify under the registered public key.
var ErrInvalidSignature = errors.New("server: invalid signature")
