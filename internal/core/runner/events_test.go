package runner

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/event"
)

func TestRegisterEventsRoundTripsAllTypes(t *testing.T) {
	t.Parallel()

	r := event.NewRegistry()
	RegisterEvents(r)

	now := time.Unix(1700000000, 0)
	cases := []any{
		RunStartedEvent{RunID: "r", StartedAt: now},
		RunCompletedEvent{RunID: "r", CompletedAt: now},
		RunCancelledEvent{RunID: "r", CancelledAt: now, Reason: "user cancel"},
		RunAbortedEvent{RunID: "r", AbortedAt: now, NodeID: "x", Reason: "boom"},
		NodeStartedEvent{RunID: "r", NodeID: "x", Attempt: 1, StartedAt: now},
		NodeSucceededEvent{RunID: "r", NodeID: "x", Attempt: 1, CompletedAt: now, Output: json.RawMessage(`{"ok":true}`)},
		NodeFailedEvent{RunID: "r", NodeID: "x", Attempt: 1, FailedAt: now, Error: "bad"},
		NodeRetriedEvent{RunID: "r", NodeID: "x", NextAttempt: 2, BackoffFor: time.Second},
		PermissionRequestedEvent{RunID: "r", NodeID: "x", RequestID: "p", Tool: "edit", RequestedAt: now},
		PermissionGrantedEvent{RunID: "r", NodeID: "x", RequestID: "p", GrantedAt: now},
		PermissionDeniedEvent{RunID: "r", NodeID: "x", RequestID: "p", DeniedAt: now, Reason: "no"},
	}

	for _, want := range cases {
		typ, ver, err := classifyEvent(want)
		if err != nil {
			t.Fatalf("classifyEvent %T: %v", want, err)
		}
		payload, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal %T: %v", want, err)
		}
		decoded, err := r.Decode(event.Envelope{Type: typ, Version: ver, Payload: payload})
		if err != nil {
			t.Fatalf("decode %T: %v", want, err)
		}
		// The decoded value must be the same dynamic type as the
		// original; payload-by-payload compare via JSON.
		gotJSON, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
		if string(gotJSON) != string(payload) {
			t.Fatalf("round-trip drift for %T:\n got %s\nwant %s", want, gotJSON, payload)
		}
	}
}

func TestUnknownEventTypeStillSkipped(t *testing.T) {
	t.Parallel()

	r := event.NewRegistry()
	RegisterEvents(r)

	_, err := r.Decode(event.Envelope{Type: "future.event", Version: 1, Payload: json.RawMessage(`{}`)})
	if !errors.Is(err, event.ErrSkipUnknownType) {
		t.Fatalf("unknown event: got %v want ErrSkipUnknownType", err)
	}
}

func TestClassifyUnknownReturnsError(t *testing.T) {
	t.Parallel()

	if _, _, err := classifyEvent(struct{}{}); err == nil {
		t.Fatal("classifyEvent on unknown: want error")
	}
}
