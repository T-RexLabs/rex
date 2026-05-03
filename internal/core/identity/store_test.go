package identity

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "identity"))
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	original, err := GenerateKeypair("alice", &deterministicReader{seed: 21})
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if err := store.Save(original, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load("alice")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(loaded.Public, original.Public) {
		t.Fatal("public key drifted")
	}
	if !bytes.Equal(loaded.Private, original.Private) {
		t.Fatal("private key drifted")
	}
}

func TestStoreSaveRefusesOverwriteByDefault(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 22})
	if err := store.Save(k, false); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := store.Save(k, false); err == nil {
		t.Fatal("second Save without overwrite must fail")
	}
	if err := store.Save(k, true); err != nil {
		t.Fatalf("Save with overwrite: %v", err)
	}
}

func TestStorePrivatePerms(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics do not apply on Windows")
	}
	store := newStore(t)
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 23})
	if err := store.Save(k, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(store.privatePath("alice"))
	if err != nil {
		t.Fatalf("stat private: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private mode: got %o want 0600", got)
	}
}

func TestStoreLoadSigner(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 24})
	if err := store.Save(k, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	signer, err := store.LoadSigner("alice")
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	sig, err := signer.Sign(context.Background(), []byte("hi"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !Verify(signer.PublicKey(), []byte("hi"), sig) {
		t.Fatal("loaded signer signature failed to verify")
	}
}

func TestStoreList(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	for _, h := range []Handle{"alice", "bob", "carol"} {
		k, _ := GenerateKeypair(h, &deterministicReader{seed: byte(len(h))})
		if err := store.Save(k, false); err != nil {
			t.Fatalf("Save %q: %v", h, err)
		}
	}
	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []Handle{"alice", "bob", "carol"}
	if len(got) != len(want) {
		t.Fatalf("len: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestStoreListIgnoresOrphanedKey(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 25})
	if err := store.Save(k, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Remove the .pub so alice is half-installed.
	if err := os.Remove(store.publicPath("alice")); err != nil {
		t.Fatalf("remove pub: %v", err)
	}
	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("orphaned key listed: %v", got)
	}
}

func TestStoreRemove(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	k, _ := GenerateKeypair("alice", &deterministicReader{seed: 26})
	if err := store.Save(k, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Remove("alice"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(store.privatePath("alice")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("private not removed: %v", err)
	}
}

func TestStoreRemoveMissing(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	err := store.Remove("ghost")
	if err == nil {
		t.Fatal("remove of unknown handle should error")
	}
}

func TestStoreLoadDetectsMismatchedPair(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	a, _ := GenerateKeypair("alice", &deterministicReader{seed: 27})
	b, _ := GenerateKeypair("alice", &deterministicReader{seed: 99})
	if err := store.Save(a, false); err != nil {
		t.Fatalf("Save a: %v", err)
	}
	// Overwrite the .pub with b's public material so the stored
	// pair is internally inconsistent.
	pubPEM, _ := MarshalPublicPEM(b)
	if err := os.WriteFile(store.publicPath("alice"), pubPEM, 0o644); err != nil {
		t.Fatalf("rewrite pub: %v", err)
	}
	if _, err := store.Load("alice"); err == nil {
		t.Fatal("Load should detect pub/priv mismatch")
	}
}

func TestStoreEnsureDirCreates(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", "identity")
	store := NewStore(dir)
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("dir mode: got %o want 0700", got)
		}
	}
}

func TestDefaultStoreDirReturnsValidPath(t *testing.T) {
	t.Parallel()

	got, err := DefaultStoreDir()
	if err != nil {
		t.Fatalf("DefaultStoreDir: %v", err)
	}
	if got == "" {
		t.Fatal("DefaultStoreDir returned empty path")
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("DefaultStoreDir not absolute: %q", got)
	}
}
