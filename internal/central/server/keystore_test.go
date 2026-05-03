package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
)

func writeKeysFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func mintKey(t *testing.T) (identity.Keypair, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	kp := identity.Keypair{Handle: "alice", Public: pub, Private: priv}
	return kp, pub, priv
}

func TestLoadKeystoreFileOK(t *testing.T) {
	t.Parallel()

	kp, _, _ := mintKey(t)
	pem, err := identity.MarshalPublicPEM(kp)
	if err != nil {
		t.Fatalf("MarshalPublicPEM: %v", err)
	}
	body := `
[[keys]]
handle = "alice"
public_key_pem = """
` + string(pem) + `
"""
`
	path := writeKeysFile(t, body)
	ks, err := LoadKeystoreFile(path)
	if err != nil {
		t.Fatalf("LoadKeystoreFile: %v", err)
	}
	if got := ks.Handles(); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("handles: %v", got)
	}
	if _, ok := ks.Lookup(kp.Fingerprint()); !ok {
		t.Fatal("alice's fingerprint should be registered")
	}
}

func TestLoadKeystoreFileMismatchedFingerprint(t *testing.T) {
	t.Parallel()

	kp, _, _ := mintKey(t)
	pem, _ := identity.MarshalPublicPEM(kp)
	body := `
[[keys]]
handle = "alice"
fingerprint = "0000000000000000"
public_key_pem = """
` + string(pem) + `
"""
`
	path := writeKeysFile(t, body)
	_, err := LoadKeystoreFile(path)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestLoadKeystoreFileMissing(t *testing.T) {
	t.Parallel()

	_, err := LoadKeystoreFile(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatal("expected error on missing file")
	}
}

func TestLoadKeystoreFileMalformedPEM(t *testing.T) {
	t.Parallel()

	body := `
[[keys]]
handle = "alice"
public_key_pem = "not a pem block"
`
	path := writeKeysFile(t, body)
	_, err := LoadKeystoreFile(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadKeystoreFileDuplicateFingerprint(t *testing.T) {
	t.Parallel()

	kp, _, _ := mintKey(t)
	pem, _ := identity.MarshalPublicPEM(kp)
	body := `
[[keys]]
handle = "alice"
public_key_pem = """
` + string(pem) + `
"""

[[keys]]
handle = "alice-clone"
public_key_pem = """
` + string(pem) + `
"""
`
	path := writeKeysFile(t, body)
	_, err := LoadKeystoreFile(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestKeystoreVerifyHappyPath(t *testing.T) {
	t.Parallel()

	_, pub, priv := mintKey(t)
	ks := NewKeystore()
	fp, err := ks.Add("alice", pub)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	canonical := []byte("payload to sign")
	sig := ed25519.Sign(priv, canonical)
	if err := ks.Verify(fp, canonical, sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestKeystoreVerifyUnknownIdentity(t *testing.T) {
	t.Parallel()

	ks := NewKeystore()
	var fp identity.Fingerprint
	for i := range fp {
		fp[i] = 0xab
	}
	err := ks.Verify(fp, []byte("x"), []byte("y"))
	if !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("got %v want ErrUnknownIdentity", err)
	}
}

func TestKeystoreVerifyInvalidSignature(t *testing.T) {
	t.Parallel()

	_, pub, _ := mintKey(t)
	ks := NewKeystore()
	fp, _ := ks.Add("alice", pub)
	err := ks.Verify(fp, []byte("payload"), []byte("not a real signature"))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("got %v want ErrInvalidSignature", err)
	}
}

func TestKeystoreEmpty(t *testing.T) {
	t.Parallel()

	ks := NewKeystore()
	if !ks.Empty() {
		t.Fatal("fresh keystore should be empty")
	}
	_, pub, _ := mintKey(t)
	_, _ = ks.Add("alice", pub)
	if ks.Empty() {
		t.Fatal("keystore with one key should not be empty")
	}
}

func TestKeystoreAddRejectsNil(t *testing.T) {
	t.Parallel()

	if _, err := NewKeystore().Add("x", nil); err == nil {
		t.Fatal("nil public key should error")
	}
}
