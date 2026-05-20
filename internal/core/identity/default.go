package identity

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// DefaultHandle is the handle used when no identity is configured
// and the caller asks for "the default". Picked because it sorts
// well in `rex identity list` and avoids accidentally colliding
// with a user's chosen handle.
const DefaultHandle Handle = "default"

// ensureDefaultMu serializes EnsureDefaultStoreSigner across
// goroutines in the same process, so two concurrent callers don't
// both generate-and-save (one succeeds with overwrite=false, the
// other fails with "already exists"). Cross-process races are
// outside scope; concurrent `rex init` against the same
// identity store from two processes is exotic enough to leave
// unspecified.
var ensureDefaultMu sync.Mutex

// EnsureDefaultStoreSigner loads the default identity from a Store,
// generating one if it does not yet exist. Returns a Signer ready
// to sign records.
//
// First-run flow: when a fresh local node calls
// EnsureDefaultStoreSigner, a new ed25519 keypair is generated, saved
// under <store>/default.{key,pub}, and returned. Subsequent calls
// load the persisted material.
func EnsureDefaultStoreSigner(s *Store) (Signer, error) {
	if s == nil {
		return nil, errors.New("identity: EnsureDefaultStoreSigner needs a non-nil Store")
	}
	ensureDefaultMu.Lock()
	defer ensureDefaultMu.Unlock()

	signer, err := s.LoadSigner(DefaultHandle)
	if err == nil {
		return signer, nil
	}
	// Either the file does not exist or it is corrupt; for first
	// run, generate a new keypair. Corrupt-key cases will surface
	// when the new Save fails (overwrite=false) — the caller can
	// then decide whether to remove and retry.
	kp, err := GenerateKeypair(DefaultHandle, nil)
	if err != nil {
		return nil, fmt.Errorf("identity: generate default keypair: %w", err)
	}
	if err := s.Save(kp, false); err != nil {
		return nil, fmt.Errorf("identity: save default keypair: %w", err)
	}
	return NewMemorySigner(kp)
}

// SignFunc adapts a Signer to the function-typed signer the eventlog
// package uses. ctx is fixed to context.Background since eventlog
// has no notion of context — that's intentional, the storage layer
// stays a leaf primitive.
func SignFunc(s Signer) func(payload []byte) ([]byte, error) {
	if s == nil {
		return nil
	}
	return func(payload []byte) ([]byte, error) {
		return s.Sign(context.Background(), payload)
	}
}
