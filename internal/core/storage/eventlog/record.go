package eventlog

import "encoding/json"

// Record is the on-disk shape of one row in events.log per
// storage.EVENTS.2. Every persisted Rex event materializes as a Record;
// the inner Payload is whatever the producing subsystem registered with
// internal/core/event.
//
// The struct is intentionally flat — no nested envelope — so a JSON
// reader can stream-decode it without first peeking at a header byte.
type Record struct {
	ID          string          `json:"id"`
	Timestamp   HLC             `json:"timestamp"`
	Type        string          `json:"type"`
	Version     uint32          `json:"version"`
	Actor       string          `json:"actor"`
	WorkspaceID string          `json:"workspace_id"`
	Payload     json.RawMessage `json:"payload"`
}
