package acp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the JSON-RPC version string Rex sends and expects.
const Version = "2.0"

// Message is the JSON-RPC 2.0 envelope, accepting either request,
// notification, or response shapes. Rex code only inspects the
// classification helpers (IsRequest/IsNotification/IsResponse) — never
// the raw zero-values of fields, since omitempty is what distinguishes
// the variants on the wire.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether m has a method and an id, i.e. expects a
// response.
func (m Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification reports whether m has a method but no id, i.e. a
// fire-and-forget message.
func (m Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// IsResponse reports whether m carries a result/error rather than a
// method invocation. Response messages always have an id.
func (m Message) IsResponse() bool { return m.Method == "" }

// Error is the JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil acp error>"
	}
	if len(e.Data) == 0 {
		return fmt.Sprintf("acp: rpc error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("acp: rpc error %d: %s (data=%s)", e.Code, e.Message, e.Data)
}

// JSON-RPC 2.0 reserved error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603
)

// NewRequest builds a Message representing a request with a positive
// integer ID. Params may be nil for parameterless methods.
func NewRequest(id int64, method string, params any) (Message, error) {
	if method == "" {
		return Message{}, errors.New("acp: NewRequest method is required")
	}
	idBytes, err := json.Marshal(id)
	if err != nil {
		return Message{}, fmt.Errorf("acp: marshal id: %w", err)
	}
	var paramsBytes json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Message{}, fmt.Errorf("acp: marshal params: %w", err)
		}
		paramsBytes = b
	}
	return Message{
		JSONRPC: Version,
		ID:      idBytes,
		Method:  method,
		Params:  paramsBytes,
	}, nil
}

// NewNotification builds a Message representing a notification (no id).
func NewNotification(method string, params any) (Message, error) {
	if method == "" {
		return Message{}, errors.New("acp: NewNotification method is required")
	}
	var paramsBytes json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Message{}, fmt.Errorf("acp: marshal params: %w", err)
		}
		paramsBytes = b
	}
	return Message{
		JSONRPC: Version,
		Method:  method,
		Params:  paramsBytes,
	}, nil
}

// NewResponse builds a successful response.
func NewResponse(id json.RawMessage, result any) (Message, error) {
	if len(id) == 0 {
		return Message{}, errors.New("acp: NewResponse id is required")
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return Message{}, fmt.Errorf("acp: marshal result: %w", err)
	}
	return Message{
		JSONRPC: Version,
		ID:      id,
		Result:  resultBytes,
	}, nil
}

// NewErrorResponse builds an error response.
func NewErrorResponse(id json.RawMessage, code int, message string, data any) (Message, error) {
	if len(id) == 0 {
		return Message{}, errors.New("acp: NewErrorResponse id is required")
	}
	var dataBytes json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Message{}, fmt.Errorf("acp: marshal error data: %w", err)
		}
		dataBytes = b
	}
	return Message{
		JSONRPC: Version,
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
			Data:    dataBytes,
		},
	}, nil
}
