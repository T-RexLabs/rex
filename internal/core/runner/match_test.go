package runner

import (
	"testing"
	"time"
)

func TestMatchesRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		decoded any
		want    bool
	}{
		{"run.started", RunStartedEvent{RunID: "alpha"}, true},
		{"run.completed", RunCompletedEvent{RunID: "alpha"}, true},
		{"run.cancelled", RunCancelledEvent{RunID: "alpha"}, true},
		{"run.aborted", RunAbortedEvent{RunID: "alpha"}, true},
		{"node.started", NodeStartedEvent{RunID: "alpha"}, true},
		{"node.succeeded", NodeSucceededEvent{RunID: "alpha"}, true},
		{"node.failed", NodeFailedEvent{RunID: "alpha"}, true},
		{"node.retried", NodeRetriedEvent{RunID: "alpha"}, true},
		{"permission.requested", PermissionRequestedEvent{RunID: "alpha"}, true},
		{"permission.granted", PermissionGrantedEvent{RunID: "alpha"}, true},
		{"permission.denied", PermissionDeniedEvent{RunID: "alpha"}, true},
		{"different run", RunStartedEvent{RunID: "beta"}, false},
		{"unknown type", struct{ RunID string }{"alpha"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesRun(tc.decoded, "alpha"); got != tc.want {
				t.Fatalf("MatchesRun: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestFoldEventTracksLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	var s RunSummary
	events := []any{
		RunStartedEvent{RunID: "r1", StartedAt: now},
		NodeStartedEvent{RunID: "r1"},
		NodeSucceededEvent{RunID: "r1"},
		RunCompletedEvent{RunID: "r1", CompletedAt: now.Add(time.Second)},
	}
	for i, ev := range events {
		if !s.FoldEvent(ev) {
			t.Fatalf("event %d: FoldEvent returned false unexpectedly: %T", i, ev)
		}
	}
	if s.RunID != "r1" {
		t.Fatalf("run id: got %q", s.RunID)
	}
	if s.Status != RunStatusCompleted {
		t.Fatalf("status: got %q", s.Status)
	}
	if !s.StartedAt.Equal(now) {
		t.Fatalf("started_at: got %v", s.StartedAt)
	}
	if !s.EndedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("ended_at: got %v", s.EndedAt)
	}
	if s.NodeEvents != 2 {
		t.Fatalf("node events: got %d want 2", s.NodeEvents)
	}
}

func TestFoldEventFirstEventSetsRunID(t *testing.T) {
	t.Parallel()

	var s RunSummary
	if ok := s.FoldEvent(RunStartedEvent{RunID: "fresh"}); !ok {
		t.Fatal("FoldEvent should accept first event")
	}
	if s.RunID != "fresh" {
		t.Fatalf("run id: got %q", s.RunID)
	}
}

func TestFoldEventRejectsUnrelatedRun(t *testing.T) {
	t.Parallel()

	s := RunSummary{RunID: "alpha"}
	if ok := s.FoldEvent(RunStartedEvent{RunID: "beta"}); ok {
		t.Fatal("FoldEvent should reject unrelated run")
	}
}

func TestFoldEventRejectsUnknownType(t *testing.T) {
	t.Parallel()

	var s RunSummary
	if ok := s.FoldEvent("not an event"); ok {
		t.Fatal("FoldEvent should reject unknown type")
	}
}

func TestEffectiveStatusDefaultsToRunning(t *testing.T) {
	t.Parallel()

	var s RunSummary
	if s.EffectiveStatus() != RunStatusRunning {
		t.Fatalf("default: got %q", s.EffectiveStatus())
	}
	s.Status = RunStatusCompleted
	if s.EffectiveStatus() != RunStatusCompleted {
		t.Fatalf("explicit: got %q", s.EffectiveStatus())
	}
}
