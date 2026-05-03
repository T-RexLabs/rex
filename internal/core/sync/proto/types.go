package proto

import (
	"encoding/json"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// ProtocolVersion is the integer version both sides advertise on
// /sync/state. Within a major version, additive shape changes are
// forward-compatible (sync.PROTO.3); semantic breaks bump this
// integer.
const ProtocolVersion = 1

// HeadEmpty is the canonical id sent when the server has zero
// events. Clients should treat it as "you have no cursor; start
// from the beginning".
const HeadEmpty = ""

// StateResponse is the body of GET /sync/state (sync.API.1).
type StateResponse struct {
	// HeadID is the eventlog.Record.ID of the most recent event
	// the server has observed, or HeadEmpty.
	HeadID string `json:"head_id"`
	// Fingerprint is the central node's public-key fingerprint
	// (16 hex chars, matches identity.Fingerprint.String).
	Fingerprint string `json:"fingerprint"`
	// Actor is the canonical central actor string for this node
	// ("c-<fingerprint>"); kept alongside Fingerprint so clients
	// don't have to re-derive it.
	Actor string `json:"actor"`
	// ProtocolVersion is what this server speaks. Clients refuse to
	// proceed if their version is incompatible (sync.PROTO.2).
	ProtocolVersion int `json:"protocol_version"`
}

// PushRequest is the body of POST /sync/events (sync.API.2).
type PushRequest struct {
	// Since is the client's last-known server HEAD. Allows the
	// server to detect divergence; on mismatch the server returns
	// 409 with the diverging tail.
	Since string `json:"since"`
	// Events is a contiguous batch in HLC order. The server may
	// reject the batch if the events are not contiguous.
	Events []eventlog.Record `json:"events"`
}

// PushResponse is the success body for POST /sync/events.
type PushResponse struct {
	// HeadID is the new server HEAD after appending the batch.
	HeadID string `json:"head_id"`
	// Accepted is the number of events from the batch that were
	// newly persisted; events the server already had count as
	// no-op (sync.API.6 idempotency).
	Accepted int `json:"accepted"`
	// Duplicates is the number of events the server already had
	// and silently skipped.
	Duplicates int `json:"duplicates"`
}

// ConflictResponse is the 409 body when the client's `since` does
// not match the server's HEAD.
type ConflictResponse struct {
	ServerHead string `json:"server_head"`
	// DivergingTail is the events the server has past the
	// client's `since` cursor; the client uses this to rebase.
	DivergingTail []eventlog.Record `json:"diverging_tail"`
}

// SSEFrame is one Server-Sent Events payload from
// GET /sync/events?since=<id> (sync.API.3). The wire format is
// `data: <JSON>\n\n` per the SSE spec; this struct is the JSON the
// server emits in the data field.
type SSEFrame struct {
	Record eventlog.Record `json:"record"`
}

// ErrorResponse is the body returned for non-2xx responses outside
// of 409 (which has its own ConflictResponse).
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MarshalErrorResponse is a tiny convenience for handlers.
func MarshalErrorResponse(code, message string) []byte {
	body, _ := json.Marshal(ErrorResponse{Code: code, Message: message})
	return body
}

// Standardized error code strings.
const (
	ErrCodeBadRequest      = "bad_request"
	ErrCodeUnauthorized    = "unauthorized"
	ErrCodeIncompatibleVer = "incompatible_protocol_version"
	ErrCodeServerError     = "server_error"
	ErrCodeNonContiguous   = "non_contiguous_batch"
)
