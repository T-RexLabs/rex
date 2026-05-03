package audit

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// ErrNotAuditEvent is returned by Append when the supplied event
// type is not in the audit registry. This is the structural
// append-only-and-typed enforcement audit.STORE.2 calls for: only
// audit-class entries reach the audit appender.
var ErrNotAuditEvent = errors.New("audit: event type is not audit-class")

// Appender is the only path by which audit-class events should
// reach the event log. It wraps an eventlog.Writer; the Writer's
// own append-only file lock keeps audit.STORE.2 honest at the
// file-system level.
type Appender struct {
	w *eventlog.Writer
}

// NewAppender wraps w as an audit-class appender. The wrapped writer
// continues to be usable directly for non-audit events; this layer
// adds the type-level guarantee for the audit path.
func NewAppender(w *eventlog.Writer) *Appender {
	return &Appender{w: w}
}

// Append marshals payload as JSON and writes it through the
// underlying eventlog.Writer. Returns ErrNotAuditEvent if eventType
// is not registered.
func (a *Appender) Append(eventType string, payload any) (eventlog.Record, error) {
	if !IsAuditEvent(eventType) {
		return eventlog.Record{}, fmt.Errorf("%w: %q", ErrNotAuditEvent, eventType)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Record{}, fmt.Errorf("audit: marshal %s: %w", eventType, err)
	}
	rec, err := a.w.Append(eventType, EventVersion, body)
	if err != nil {
		return eventlog.Record{}, fmt.Errorf("audit: write %s: %w", eventType, err)
	}
	return rec, nil
}
