package sync

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// openAppend opens path for append in the same mode the eventlog
// writer uses. Pull writes verbatim records (already-stamped from
// the originating node), so we cannot use eventlog.Writer.Append —
// that would re-stamp HLC and actor.
func openAppend(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sync: open events.log: %w", err)
	}
	return f, nil
}

// appendRaw writes one Record to f using the same length-prefixed
// JSON framing the eventlog package uses on disk.
func appendRaw(f *os.File, rec eventlog.Record) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("sync: marshal record: %w", err)
	}
	if len(body) > eventlog.MaxRecordSize {
		return fmt.Errorf("sync: record exceeds MaxRecordSize")
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	if _, err := f.Write(frame); err != nil {
		return fmt.Errorf("sync: write record: %w", err)
	}
	return f.Sync()
}

// readEventsAfter scans events.log and returns the records whose ids
// appear strictly after sinceID. An empty sinceID returns everything.
// Returns ErrUnknownSince when sinceID is non-empty but does not
// match any record id in the log — the caller should treat this as a
// hard divergence.
func readEventsAfter(path, sinceID string) ([]eventlog.Record, error) {
	r, err := eventlog.OpenReader(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()

	var out []eventlog.Record
	collecting := sinceID == ""
	matched := collecting
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !collecting {
			if rec.ID == sinceID {
				collecting = true
				matched = true
			}
			continue
		}
		out = append(out, rec)
	}
	if !matched && sinceID != "" {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSince, sinceID)
	}
	return out, nil
}

// ErrUnknownSince is returned by readEventsAfter when sinceID does
// not exist in the log.
var ErrUnknownSince = errors.New("sync: unknown since-id in local events.log")
