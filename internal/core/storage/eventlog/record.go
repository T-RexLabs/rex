package eventlog

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Record is the on-disk shape of one row in events.log per
// storage.EVENTS.2. Every persisted Rex event materializes as a Record;
// the inner Payload is whatever the producing subsystem registered with
// internal/core/event.
//
// The struct is intentionally flat — no nested envelope — so a JSON
// reader can stream-decode it without first peeking at a header byte.
//
// Signature is additive (overview.SYS.4): pre-signing records have an
// empty Signature; readers that don't care about authenticity can
// ignore the field. sync.SEC.2 covers id, type, timestamp, version,
// actor, workspace_id, and payload — exactly the input to
// SigningBytes below.
type Record struct {
	ID          string          `json:"id"`
	Timestamp   HLC             `json:"timestamp"`
	Type        string          `json:"type"`
	Version     uint32          `json:"version"`
	Actor       string          `json:"actor"`
	WorkspaceID string          `json:"workspace_id"`
	Payload     json.RawMessage `json:"payload"`
	Signature   string          `json:"signature,omitempty"`
}

// SigningBytes returns the canonical byte slice over which a Record's
// signature is computed. The encoding is the same JSON form
// json.Marshal produces for a Record with the Signature field
// stripped — recomputing SigningBytes on a received Record requires
// no canonicalization machinery beyond stdlib JSON.
//
// Per sync.SEC.2, the signature input covers id, type, timestamp,
// version, actor, workspace_id, and payload. Signature itself is
// excluded so a verifier can rederive the input from what was
// signed.
func SigningBytes(rec Record) ([]byte, error) {
	rec.Signature = ""
	body, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("eventlog: marshal for signing: %w", err)
	}
	return body, nil
}

// VerifyRecord checks rec's Signature against verifyFn (typically
// backed by ed25519.Verify with the actor's known public key).
// Returns nil on success, an error on missing or invalid signature.
func VerifyRecord(rec Record, verifyFn func(payload, sig []byte) bool) error {
	if rec.Signature == "" {
		return fmt.Errorf("eventlog: record %q has no signature", rec.ID)
	}
	body, err := SigningBytes(rec)
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(rec.Signature)
	if err != nil {
		return fmt.Errorf("eventlog: decode signature for %q: %w", rec.ID, err)
	}
	if !verifyFn(body, sig) {
		return fmt.Errorf("eventlog: signature on %q does not verify", rec.ID)
	}
	return nil
}
