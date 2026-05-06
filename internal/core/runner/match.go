package runner

import "time"

// MatchesRun reports whether the decoded runner event references
// runID. Used by cli/run, internal/local/web, and any other surface
// that needs a single source of truth for "is this event part of
// the run with id X?". Single point of update when new event types
// land in the registry.
func MatchesRun(decoded any, runID string) bool {
	switch ev := decoded.(type) {
	case RunStartedEvent:
		return ev.RunID == runID
	case RunCompletedEvent:
		return ev.RunID == runID
	case RunCancelledEvent:
		return ev.RunID == runID
	case RunAbortedEvent:
		return ev.RunID == runID
	case NodeStartedEvent:
		return ev.RunID == runID
	case NodeSucceededEvent:
		return ev.RunID == runID
	case NodeFailedEvent:
		return ev.RunID == runID
	case NodeRetriedEvent:
		return ev.RunID == runID
	case PermissionRequestedEvent:
		return ev.RunID == runID
	case PermissionGrantedEvent:
		return ev.RunID == runID
	case PermissionDeniedEvent:
		return ev.RunID == runID
	case HarnessFrameEvent:
		return ev.RunID == runID
	}
	return false
}

// RunSummary is the cross-package view of a run's lifecycle: id,
// final status (or RunStatusRunning when still in flight),
// timestamps, and a count of node-class events. The run-list pages
// in cli, web, and any future surface fold events into one of
// these.
type RunSummary struct {
	RunID      string
	Status     RunStatus
	StartedAt  time.Time
	EndedAt    time.Time
	NodeEvents int
	// SpecRefs and FromTask are folded from RunStartedEvent and let
	// list/show surfaces filter or display run provenance
	// (execution.RUN.1.1, execution.RUN.1.2).
	SpecRefs []string
	FromTask string
}

// FoldEvent applies a decoded runner event to the summary. Returns
// true when the event referenced this summary's RunID and was
// applied; false otherwise (caller should ignore unrelated events
// rather than crashing). When the summary's RunID is empty, the
// first event seen sets it — useful when callers fold a stream and
// don't know the id ahead of time.
func (s *RunSummary) FoldEvent(decoded any) bool {
	id := summaryRunID(decoded)
	if id == "" {
		return false
	}
	if s.RunID == "" {
		s.RunID = id
	} else if s.RunID != id {
		return false
	}

	switch ev := decoded.(type) {
	case RunStartedEvent:
		if s.StartedAt.IsZero() {
			s.StartedAt = ev.StartedAt
		}
		if len(s.SpecRefs) == 0 && len(ev.SpecRefs) > 0 {
			s.SpecRefs = append(s.SpecRefs, ev.SpecRefs...)
		}
		if s.FromTask == "" {
			s.FromTask = ev.FromTask
		}
	case RunCompletedEvent:
		s.Status = RunStatusCompleted
		s.EndedAt = ev.CompletedAt
	case RunCancelledEvent:
		s.Status = RunStatusCancelled
		s.EndedAt = ev.CancelledAt
	case RunAbortedEvent:
		s.Status = RunStatusAborted
		s.EndedAt = ev.AbortedAt
	case NodeStartedEvent:
		s.NodeEvents++
	case NodeSucceededEvent:
		s.NodeEvents++
	case NodeFailedEvent:
		s.NodeEvents++
	case NodeRetriedEvent:
		s.NodeEvents++
	}
	return true
}

// summaryRunID extracts the RunID field from any of the runner
// event payload types. Centralized here so MatchesRun and FoldEvent
// stay aligned on which event types belong to a run.
func summaryRunID(decoded any) string {
	switch ev := decoded.(type) {
	case RunStartedEvent:
		return ev.RunID
	case RunCompletedEvent:
		return ev.RunID
	case RunCancelledEvent:
		return ev.RunID
	case RunAbortedEvent:
		return ev.RunID
	case NodeStartedEvent:
		return ev.RunID
	case NodeSucceededEvent:
		return ev.RunID
	case NodeFailedEvent:
		return ev.RunID
	case NodeRetriedEvent:
		return ev.RunID
	case PermissionRequestedEvent:
		return ev.RunID
	case PermissionGrantedEvent:
		return ev.RunID
	case PermissionDeniedEvent:
		return ev.RunID
	}
	return ""
}

// EffectiveStatus is the run's status defaulting to Running when no
// terminal event has been folded yet. Lets table renderers display
// in-flight runs without an explicit "if status == ” then Running"
// dance at every callsite.
func (s RunSummary) EffectiveStatus() RunStatus {
	if s.Status == "" {
		return RunStatusRunning
	}
	return s.Status
}
