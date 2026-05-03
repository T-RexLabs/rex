package sync

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// CountEventsAfter returns the number of records in events.log past
// sinceID. An empty sinceID counts every record. Missing log files
// yield 0 + nil; ErrUnknownSince is returned when sinceID is
// non-empty but does not match any record id.
func CountEventsAfter(eventsLogPath, sinceID string) (int, error) {
	r, err := eventlog.OpenReader(eventsLogPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer r.Close()

	count := 0
	collecting := sinceID == ""
	matched := collecting
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		if !collecting {
			if rec.ID == sinceID {
				collecting = true
				matched = true
			}
			continue
		}
		count++
	}
	if !matched && sinceID != "" {
		return 0, fmt.Errorf("%w: %q", ErrUnknownSince, sinceID)
	}
	return count, nil
}

// ListWatermarks enumerates per-remote watermark files under
// <workspaceRoot>/.rex/drafts/. Returns Watermark structs in lex
// order by remote name; missing directory yields an empty slice.
func ListWatermarks(workspaceRoot string) ([]Watermark, error) {
	dir := filepath.Join(workspaceRoot, ".rex", DraftsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sync: list watermarks: %w", err)
	}
	out := make([]Watermark, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		remote := strings.TrimSuffix(name, ".toml")
		wm, err := LoadWatermark(workspaceRoot, remote)
		if err != nil {
			return nil, err
		}
		out = append(out, wm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Remote < out[j].Remote })
	return out, nil
}
