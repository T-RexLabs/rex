package runner

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// CancelWatchPollInterval is the file-tail cadence the watcher
// uses to look for new RunCancellationRequested events. Mirrors
// the web-UI / CLI attach polling cadence (web-ui.LIVE.1-note,
// run.go's runWatchPollInterval) so the cancel-latency feel
// matches the live-view feel.
const CancelWatchPollInterval = 100 * time.Millisecond

// WatchForCancel tails events.log for run.cancellation_requested
// events targeting runID. On match it invokes cancel() and
// returns. Returns when ctx is cancelled (the run completed or
// the caller aborted the watch) without ever invoking cancel().
//
// The watcher is safe to run alongside the executor in the same
// process: eventlog readers do not lock; writers use an exclusive
// file lock during append, so partial-record races are impossible.
//
// The poll loop is deliberately simple — open reader, scan to
// EOF, sleep, re-open. v1 has no daemon, no in-process bus, so
// the file is the channel.
func WatchForCancel(
	ctx context.Context,
	logPath, runID string,
	cancel context.CancelCauseFunc,
) {
	// Track which event IDs we've already seen so a re-open
	// doesn't re-fire on the same record. The map grows with the
	// log; for v1 workspaces (well under 100k events) this is
	// fine. A future optimization can carry the byte offset
	// across iterations.
	seen := make(map[string]struct{})
	reg := event.NewRegistry()
	RegisterEvents(reg)

	scan := func() bool {
		r, err := eventlog.OpenReader(logPath)
		if err != nil {
			// Pre-first-event state is fine; surface other
			// errors as a watch-cannot-continue signal.
			return false
		}
		defer r.Close()
		for {
			rec, err := r.Next()
			if errors.Is(err, io.EOF) {
				return false
			}
			if err != nil {
				return false
			}
			if rec.Type != EventTypeRunCancellationRequested {
				continue
			}
			if _, dup := seen[rec.ID]; dup {
				continue
			}
			seen[rec.ID] = struct{}{}
			decoded, derr := reg.Decode(event.Envelope{
				Type:    rec.Type,
				Version: rec.Version,
				Payload: rec.Payload,
			})
			if derr != nil {
				continue
			}
			req, ok := decoded.(RunCancellationRequestedEvent)
			if !ok || req.RunID != runID {
				continue
			}
			reason := req.Reason
			if reason == "" {
				reason = "rex run cancel"
			}
			cancel(&CancelRequestedError{Requester: req.Requester, Reason: reason})
			return true
		}
	}

	if scan() {
		return
	}
	ticker := time.NewTicker(CancelWatchPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if scan() {
				return
			}
		}
	}
}

// CancelRequestedError is the cause the watcher hands to
// context.CancelCauseFunc. Callers (typically the executor's
// checkCancel path) can errors.As this to distinguish a
// user-issued cancel from other context cancellations.
type CancelRequestedError struct {
	Requester string
	Reason    string
}

func (c *CancelRequestedError) Error() string {
	if c == nil {
		return "run cancellation requested"
	}
	if c.Reason == "" {
		return "run cancellation requested"
	}
	return "run cancellation requested: " + c.Reason
}
