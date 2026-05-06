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

// TestRunStartedEventProvenanceFieldsRoundTrip covers
// execution.RUN.1.1 — the optional spec_refs and from_task fields
// must round-trip through the registry without loss.
func TestRunStartedEventProvenanceFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	r := event.NewRegistry()
	RegisterEvents(r)

	now := time.Unix(1700000000, 0)
	want := RunStartedEvent{
		RunID:     "r",
		StartedAt: now,
		SpecRefs:  []string{"sync.ORDER.3", "overview.SEC.2"},
		FromTask:  "spec-format.define-run-recipes",
	}

	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := r.Decode(event.Envelope{Type: EventTypeRunStarted, Version: EventVersion, Payload: payload})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := decoded.(RunStartedEvent)
	if !ok {
		t.Fatalf("decoded type: got %T", decoded)
	}
	if got.RunID != want.RunID || !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("base fields drift: got %+v", got)
	}
	if got.FromTask != want.FromTask {
		t.Fatalf("from_task: got %q want %q", got.FromTask, want.FromTask)
	}
	if len(got.SpecRefs) != len(want.SpecRefs) {
		t.Fatalf("spec_refs len: got %v want %v", got.SpecRefs, want.SpecRefs)
	}
	for i := range want.SpecRefs {
		if got.SpecRefs[i] != want.SpecRefs[i] {
			t.Fatalf("spec_refs[%d]: got %q want %q", i, got.SpecRefs[i], want.SpecRefs[i])
		}
	}
}

// TestRunStartedEventOmitsEmptyProvenance — emitter compatibility:
// runs with no provenance produce JSON without the new keys, so old
// readers that didn't know about the fields see nothing changed.
func TestRunStartedEventOmitsEmptyProvenance(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	got, err := json.Marshal(RunStartedEvent{RunID: "r", StartedAt: now})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"spec_refs"`, `"from_task"`} {
		if contains(string(got), key) {
			t.Fatalf("expected %s omitted from empty event: %s", key, got)
		}
	}
}

// TestRunStartedEventForwardCompatExtraField — readers built before
// any later additive field landed must skip unknown JSON keys per
// overview.SYS.3.
func TestRunStartedEventForwardCompatExtraField(t *testing.T) {
	t.Parallel()

	r := event.NewRegistry()
	RegisterEvents(r)

	payload := []byte(`{"run_id":"r","started_at":"2024-01-01T00:00:00Z","unknown_future_field":42}`)
	decoded, err := r.Decode(event.Envelope{Type: EventTypeRunStarted, Version: EventVersion, Payload: payload})
	if err != nil {
		t.Fatalf("decode with unknown field: %v", err)
	}
	if got := decoded.(RunStartedEvent).RunID; got != "r" {
		t.Fatalf("run_id: got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
