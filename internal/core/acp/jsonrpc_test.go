package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "request",
			msg:  Message{JSONRPC: Version, ID: json.RawMessage(`1`), Method: "session/new"},
			want: "request",
		},
		{
			name: "notification",
			msg:  Message{JSONRPC: Version, Method: "session/update"},
			want: "notification",
		},
		{
			name: "response",
			msg:  Message{JSONRPC: Version, ID: json.RawMessage(`1`), Result: json.RawMessage(`{}`)},
			want: "response",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.msg)
			if got != tc.want {
				t.Fatalf("classify: got %s want %s", got, tc.want)
			}
		})
	}
}

func classify(m Message) string {
	switch {
	case m.IsRequest():
		return "request"
	case m.IsNotification():
		return "notification"
	case m.IsResponse():
		return "response"
	}
	return "unknown"
}

func TestNewRequestMarshalsParams(t *testing.T) {
	t.Parallel()

	type params struct {
		Prompt string `json:"prompt"`
	}
	msg, err := NewRequest(7, "session/prompt", params{Prompt: "hi"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if msg.Method != "session/prompt" || string(msg.ID) != "7" {
		t.Fatalf("unexpected envelope: %+v", msg)
	}
	if !strings.Contains(string(msg.Params), `"prompt":"hi"`) {
		t.Fatalf("params not marshaled: %s", msg.Params)
	}
}

func TestNewNotificationOmitsID(t *testing.T) {
	t.Parallel()

	msg, err := NewNotification("session/update", map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("NewNotification: %v", err)
	}
	if !msg.IsNotification() {
		t.Fatalf("classification: got %+v", msg)
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), `"id"`) {
		t.Fatalf("notification leaked id field: %s", encoded)
	}
}

func TestErrorErrorString(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeInvalidParams, Message: "missing prompt"}
	if !strings.Contains(e.Error(), "missing prompt") {
		t.Fatalf("Error() missing message: %s", e.Error())
	}

	e.Data = json.RawMessage(`{"hint":"see docs"}`)
	if !strings.Contains(e.Error(), "data=") {
		t.Fatalf("Error() with data missing data: %s", e.Error())
	}

	var nilErr *Error
	if got := nilErr.Error(); got != "<nil acp error>" {
		t.Fatalf("nil Error() got %q", got)
	}
}

func TestNewErrorResponseRequiresID(t *testing.T) {
	t.Parallel()

	if _, err := NewErrorResponse(nil, ErrCodeInternalError, "x", nil); err == nil {
		t.Fatal("nil id: want error")
	}
}
