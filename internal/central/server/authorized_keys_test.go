package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
)

// mintKeyPEM mints a fresh ed25519 keypair and returns
// (handle, fingerprint string, public PEM bytes). Used by the
// redeem + authorized-keys tests that need a PEM the server can
// parse end-to-end.
func mintKeyPEM(t *testing.T, handle string) (string, []byte) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fp, err := identity.FingerprintOf(pub)
	if err != nil {
		t.Fatalf("FingerprintOf: %v", err)
	}
	pem, err := identity.MarshalPublicPEM(identity.Keypair{Public: pub})
	if err != nil {
		t.Fatalf("MarshalPublicPEM: %v", err)
	}
	_ = handle
	return fp.String(), pem
}

// TestRegisterAuthorizedKeyInsertsAndIsIdempotent covers the
// upsert: first call inserts (inserted=true), second call with
// the same fingerprint is a no-op (inserted=false).
func TestRegisterAuthorizedKeyInsertsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	fp, pem := mintKeyPEM(t, "alice")
	ctx := context.Background()

	ins1, err := store.RegisterAuthorizedKey(ctx, fp, "alice", string(pem), "invite-redeem", "")
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if !ins1 {
		t.Error("first register inserted=false; want true")
	}
	ins2, err := store.RegisterAuthorizedKey(ctx, fp, "alice", string(pem), "invite-redeem", "")
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if ins2 {
		t.Error("second register inserted=true; want false (already registered)")
	}
}

// TestListAuthorizedKeysReturnsRegistered covers the read side:
// rows registered earlier show up in the list, sorted by
// fingerprint (the SQL ORDER clause).
func TestListAuthorizedKeysReturnsRegistered(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := context.Background()
	fpA, pemA := mintKeyPEM(t, "alice")
	fpB, pemB := mintKeyPEM(t, "bob")
	for _, args := range []struct {
		fp, handle, pem string
	}{
		{fpA, "alice", string(pemA)},
		{fpB, "bob", string(pemB)},
	} {
		if _, err := store.RegisterAuthorizedKey(ctx, args.fp, args.handle, args.pem, "invite-redeem", ""); err != nil {
			t.Fatalf("Register %s: %v", args.handle, err)
		}
	}
	rows, err := store.ListAuthorizedKeys(ctx)
	if err != nil {
		t.Fatalf("ListAuthorizedKeys: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d want 2", len(rows))
	}
	gotHandles := []string{rows[0].Handle, rows[1].Handle}
	wantSet := map[string]bool{"alice": true, "bob": true}
	for _, h := range gotHandles {
		if !wantSet[h] {
			t.Errorf("unexpected handle %q in list", h)
		}
	}
}

// TestLoadAuthorizedKeysIntoKeystoreOverlaysRows covers the
// startup overlay: rows in authorized_keys land in the supplied
// Keystore as if they came from a TOML --keys file.
func TestLoadAuthorizedKeysIntoKeystoreOverlaysRows(t *testing.T) {
	t.Parallel()
	store, _ := freshPostgresStore(t)
	ctx := context.Background()
	fp, pem := mintKeyPEM(t, "alice")
	if _, err := store.RegisterAuthorizedKey(ctx, fp, "alice", string(pem), "invite-redeem", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ks := NewKeystore()
	loaded, err := LoadAuthorizedKeysIntoKeystore(ctx, ks, store, nil)
	if err != nil {
		t.Fatalf("LoadAuthorizedKeysIntoKeystore: %v", err)
	}
	if loaded != 1 {
		t.Errorf("loaded: got %d want 1", loaded)
	}
	parsed, _ := identity.ParseFingerprint(fp)
	if _, ok := ks.Lookup(parsed); !ok {
		t.Errorf("keystore is missing the overlaid fingerprint %s", fp)
	}
}
