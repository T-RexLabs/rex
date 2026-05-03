// Package event defines the framing every persisted Rex event uses.
//
// The contract comes from overview.SYS.3 and storage.EVENTS.5: every event
// carries a Type and a Version; readers skip unknown Types and refuse
// known Types whose Version exceeds what they implement. Schema evolution
// is additive only (overview.SYS.4) — new fields are fine, but a Version
// bump is required when the meaning of an existing field changes.
//
// This package only defines the envelope and a registry for decoders. The
// concrete event payloads (workspace events, run events, audit events,
// ...) live with the subsystems that own them; each subsystem registers
// its decoders at startup.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Envelope is the wire and on-disk framing for a Rex event. The full
// on-disk record adds id/timestamp/actor/workspace_id around this
// (storage.EVENTS.2); those live in the storage layer because they are
// only meaningful for persisted events, not for in-memory ones.
type Envelope struct {
	Type    string          `json:"type"`
	Version uint32          `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

// ErrSkipUnknownType is returned by Registry.Decode when the envelope's
// Type has no registered decoder. Callers MUST treat this as "advance to
// the next event" rather than propagating the error — that is the
// forward-compatibility guarantee in overview.SYS.3.
var ErrSkipUnknownType = errors.New("event: unknown type, skip")

// ErrUnsupportedVersion is returned when the Type is known but the
// Version is higher than any registered decoder supports. This is a hard
// error: it means the reader is older than the writer for a known event
// shape, which the caller must surface (typically by refusing to start)
// rather than silently dropping data.
var ErrUnsupportedVersion = errors.New("event: unsupported version for known type")

// DecodeFunc turns a payload at a given version into a typed value. A
// decoder MAY accept multiple versions; it is the decoder's responsibility
// to upgrade older payloads to the current shape.
type DecodeFunc func(version uint32, payload []byte) (any, error)

// Registry maps event Type to a maximum supported Version and a decoder.
// A Registry is safe for concurrent reads after Register calls have
// completed; Register itself is not safe for concurrent use.
type Registry struct {
	decoders map[string]decoderEntry
}

type decoderEntry struct {
	maxVersion uint32
	fn         DecodeFunc
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{decoders: make(map[string]decoderEntry)}
}

// Register associates eventType with a decoder that handles versions up
// to and including maxVersion. Registering the same eventType twice
// overwrites the previous entry; subsystems are expected to register once
// at startup.
func (r *Registry) Register(eventType string, maxVersion uint32, fn DecodeFunc) {
	r.decoders[eventType] = decoderEntry{maxVersion: maxVersion, fn: fn}
}

// Decode resolves env to a typed value. It returns ErrSkipUnknownType for
// unregistered types and ErrUnsupportedVersion when the registered
// decoder does not understand env.Version.
func (r *Registry) Decode(env Envelope) (any, error) {
	e, ok := r.decoders[env.Type]
	if !ok {
		return nil, ErrSkipUnknownType
	}
	if env.Version > e.maxVersion {
		return nil, fmt.Errorf("%w: type=%q got=%d max=%d", ErrUnsupportedVersion, env.Type, env.Version, e.maxVersion)
	}
	return e.fn(env.Version, env.Payload)
}
