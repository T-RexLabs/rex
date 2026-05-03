package identity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// IdentityDirName is the per-user directory holding key material
// under the platform's user config root (storage.GLOBAL.3).
const IdentityDirName = "identity"

// Permissions used for written files. KEY.5 says private keys are
// mode 0600 minimum; the package writes 0600 strictly.
const (
	privateKeyMode os.FileMode = 0o600
	publicKeyMode  os.FileMode = 0o644
	identityDirMode os.FileMode = 0o700
)

// Store is a directory-backed key store. Multiple identities per
// machine are supported (KEY.3); each handle has its own .key /
// .pub pair under the same Store.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. Callers typically supply
// `<userConfigDir>/rex/identity/`; tests use a temp dir. NewStore
// does not create dir; use EnsureDir before writing.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the absolute directory path the store was opened on.
func (s *Store) Dir() string { return s.dir }

// EnsureDir creates the identity directory with restrictive perms
// if it does not exist. KEY.5 cares about private-key locality;
// hardening the parent dir is the start of that.
func (s *Store) EnsureDir() error {
	if err := os.MkdirAll(s.dir, identityDirMode); err != nil {
		return fmt.Errorf("identity: mkdir %s: %w", s.dir, err)
	}
	// Tighten perms even if MkdirAll respected an umask that left
	// them looser than identityDirMode.
	return os.Chmod(s.dir, identityDirMode)
}

// privatePath / publicPath return the canonical filenames for a
// handle's keys.
func (s *Store) privatePath(h Handle) string {
	return filepath.Join(s.dir, string(h)+".key")
}

func (s *Store) publicPath(h Handle) string {
	return filepath.Join(s.dir, string(h)+".pub")
}

// Save writes a Keypair to disk. Refuses to overwrite an existing
// pair unless overwrite is true; private-key files are written 0600.
func (s *Store) Save(k Keypair, overwrite bool) error {
	if !IsValidHandle(string(k.Handle)) {
		return fmt.Errorf("identity: handle %q is not kebab-case", k.Handle)
	}
	if err := s.EnsureDir(); err != nil {
		return err
	}
	priv := s.privatePath(k.Handle)
	pub := s.publicPath(k.Handle)
	if !overwrite {
		if _, err := os.Stat(priv); err == nil {
			return fmt.Errorf("identity: %s already exists; pass overwrite=true to replace", priv)
		}
	}
	privPEM, err := MarshalPrivatePEM(k)
	if err != nil {
		return err
	}
	pubPEM, err := MarshalPublicPEM(k)
	if err != nil {
		return err
	}
	if err := writeFileMode(priv, privPEM, privateKeyMode); err != nil {
		return err
	}
	if err := writeFileMode(pub, pubPEM, publicKeyMode); err != nil {
		// Try to clean up the private key so we don't leave a
		// partial pair.
		_ = os.Remove(priv)
		return err
	}
	return nil
}

// Load reads the keypair for handle from disk and returns it.
func (s *Store) Load(h Handle) (Keypair, error) {
	if !IsValidHandle(string(h)) {
		return Keypair{}, fmt.Errorf("identity: handle %q is not kebab-case", h)
	}
	privBody, err := os.ReadFile(s.privatePath(h))
	if err != nil {
		return Keypair{}, fmt.Errorf("identity: read private %q: %w", h, err)
	}
	priv, err := ParsePrivatePEM(privBody)
	if err != nil {
		return Keypair{}, err
	}
	pubBody, err := os.ReadFile(s.publicPath(h))
	if err != nil {
		return Keypair{}, fmt.Errorf("identity: read public %q: %w", h, err)
	}
	pub, err := ParsePublicPEM(pubBody)
	if err != nil {
		return Keypair{}, err
	}
	if !pubFromPrivateMatches(priv, pub) {
		return Keypair{}, fmt.Errorf("identity: %q .key and .pub do not match", h)
	}
	return Keypair{Handle: h, Public: pub, Private: priv}, nil
}

// LoadSigner is the common-case helper: load a keypair and wrap it
// in a Signer.
func (s *Store) LoadSigner(h Handle) (Signer, error) {
	k, err := s.Load(h)
	if err != nil {
		return nil, err
	}
	return NewMemorySigner(k)
}

// List returns all handles found in the store, in lex order.
func (s *Store) List() ([]Handle, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("identity: list %s: %w", s.dir, err)
	}
	var handles []Handle
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".key") {
			continue
		}
		h := Handle(strings.TrimSuffix(name, ".key"))
		if !IsValidHandle(string(h)) {
			continue
		}
		// Only count entries that have a matching .pub so partial
		// state from a failed Save is invisible.
		if _, err := os.Stat(s.publicPath(h)); err != nil {
			continue
		}
		handles = append(handles, h)
	}
	sort.Slice(handles, func(i, j int) bool { return handles[i] < handles[j] })
	return handles, nil
}

// Remove deletes the key pair for handle. It is intentionally a
// distinct verb from "rotate"; callers that want to replace a key
// should generate a new one and Save with overwrite=true.
func (s *Store) Remove(h Handle) error {
	if !IsValidHandle(string(h)) {
		return fmt.Errorf("identity: handle %q is not kebab-case", h)
	}
	priv := s.privatePath(h)
	pub := s.publicPath(h)
	errPriv := os.Remove(priv)
	errPub := os.Remove(pub)
	switch {
	case errPriv == nil && errPub == nil:
		return nil
	case errors.Is(errPriv, fs.ErrNotExist) && errors.Is(errPub, fs.ErrNotExist):
		return fmt.Errorf("identity: %q not found", h)
	case errPriv != nil && !errors.Is(errPriv, fs.ErrNotExist):
		return errPriv
	case errPub != nil && !errors.Is(errPub, fs.ErrNotExist):
		return errPub
	}
	return nil
}

// pubFromPrivateMatches reports whether priv's derived public key
// equals pub. ed25519.PrivateKey carries the public key as its last
// 32 bytes.
func pubFromPrivateMatches(priv []byte, pub []byte) bool {
	const ed25519PubLen = 32
	const ed25519PrivLen = 64
	if len(priv) != ed25519PrivLen || len(pub) != ed25519PubLen {
		return false
	}
	for i := 0; i < ed25519PubLen; i++ {
		if priv[ed25519PrivLen-ed25519PubLen+i] != pub[i] {
			return false
		}
	}
	return true
}

// writeFileMode atomically writes data to path with perm. Uses a
// rename so half-written files are never visible.
func writeFileMode(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rex-id-*")
	if err != nil {
		return fmt.Errorf("identity: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("identity: write %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("identity: chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("identity: close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("identity: rename %s: %w", path, err)
	}
	return nil
}

// DefaultStoreDir resolves the platform user-config-dir +
// rex/identity/. Returns an error only if the user-config-dir lookup
// fails (no $HOME, no $APPDATA, etc.).
func DefaultStoreDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("identity: locate user config dir: %w", err)
	}
	return filepath.Join(cfg, "rex", IdentityDirName), nil
}
