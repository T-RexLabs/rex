// Package eventlog implements the append-only events.log spine described
// in storage.EVENTS.
//
// File format: a sequence of records, each prefixed by 4 bytes of
// big-endian uint32 length followed by exactly that many bytes of UTF-8
// JSON. The choice of length prefix over newline-delimited JSON keeps
// the format robust to embedded newlines in payloads (transcripts
// regularly contain them) and avoids needing an escape pass on read.
//
// The writer holds an exclusive flock for the duration of each append so
// concurrent writers serialize on the file. Readers never lock: a reader
// may observe fewer records than the writer has produced, but never a
// torn record, because every Append issues exactly one os.File.Write of
// prefix+body and every record is committed atomically by the kernel
// (storage.EVENTS.4).
package eventlog

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// MaxRecordSize bounds a single record so a corrupt length prefix can't
// cause a reader to allocate unbounded memory. 16 MiB comfortably
// accommodates large ACP frames while staying small enough to detect
// corruption fast.
const MaxRecordSize = 16 * 1024 * 1024

// ErrCorruptRecord indicates the on-disk record could not be parsed —
// either a truncated length prefix, a length above MaxRecordSize, or a
// payload that does not parse as JSON.
var ErrCorruptRecord = errors.New("eventlog: corrupt record")

// WriterConfig describes the workspace and identity context a Writer
// stamps onto every Record it appends.
type WriterConfig struct {
	// Path to the events.log file. Created with 0600 permissions if it
	// does not exist; parent directory must already exist.
	Path string
	// WorkspaceID is stamped on every Record (storage.EVENTS.2).
	WorkspaceID string
	// Actor is the public-key fingerprint of the identity producing
	// these events (storage.EVENTS.2). For pre-identity bootstrap, an
	// empty actor is allowed; this will tighten once
	// identity-and-trust lands.
	Actor string
	// Clock is the HLC source. If nil, a fresh wall-clock-backed
	// Clock is created.
	Clock *Clock
}

// Writer appends records to events.log. A Writer is safe for concurrent
// use by multiple goroutines; each Append is serialized internally.
type Writer struct {
	cfg   WriterConfig
	clock *Clock

	mu sync.Mutex
	f  *os.File
}

// OpenWriter opens (or creates) the events.log at cfg.Path for appends.
func OpenWriter(cfg WriterConfig) (*Writer, error) {
	if cfg.Path == "" {
		return nil, errors.New("eventlog: WriterConfig.Path is required")
	}
	if cfg.WorkspaceID == "" {
		return nil, errors.New("eventlog: WriterConfig.WorkspaceID is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = NewClock()
	}
	f, err := os.OpenFile(cfg.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open writer: %w", err)
	}
	return &Writer{cfg: cfg, clock: clock, f: f}, nil
}

// Close releases the underlying file handle.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Append stamps a new Record with HLC + workspace + actor and writes it
// to the log. Returns the Record as written.
func (w *Writer) Append(eventType string, version uint32, payload json.RawMessage) (rec Record, err error) {
	if eventType == "" {
		return Record{}, errors.New("eventlog: event type is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f == nil {
		return Record{}, errors.New("eventlog: writer is closed")
	}

	ts := w.clock.Now()
	rec = Record{
		ID:          ts.String(),
		Timestamp:   ts,
		Type:        eventType,
		Version:     version,
		Actor:       w.cfg.Actor,
		WorkspaceID: w.cfg.WorkspaceID,
		Payload:     payload,
	}
	body, marshalErr := json.Marshal(rec)
	if marshalErr != nil {
		return Record{}, fmt.Errorf("eventlog: marshal record: %w", marshalErr)
	}
	if len(body) > MaxRecordSize {
		return Record{}, fmt.Errorf("eventlog: record exceeds MaxRecordSize (%d > %d)", len(body), MaxRecordSize)
	}

	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)

	if err = acquireExclusiveLock(w.f); err != nil {
		return Record{}, err
	}
	defer func() {
		if relErr := releaseLock(w.f); relErr != nil && err == nil {
			err = relErr
		}
	}()

	if _, err = w.f.Write(frame); err != nil {
		return Record{}, fmt.Errorf("eventlog: write: %w", err)
	}
	if err = w.f.Sync(); err != nil {
		return Record{}, fmt.Errorf("eventlog: fsync: %w", err)
	}
	return rec, nil
}

// Reader streams records out of events.log. A Reader does not lock; it
// reads whatever is currently committed and returns io.EOF when the
// physical file ends. To follow a live log, callers re-Open after EOF.
type Reader struct {
	f  *os.File
	br *bufio.Reader
}

// OpenReader opens events.log for sequential reads.
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open reader: %w", err)
	}
	return &Reader{f: f, br: bufio.NewReader(f)}, nil
}

// Close releases the file handle.
func (r *Reader) Close() error {
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Next returns the next record. It returns io.EOF cleanly when the file
// ends on a record boundary, and ErrCorruptRecord wrapped with detail
// when the file ends mid-record or the length prefix is implausible.
func (r *Reader) Next() (Record, error) {
	var lenBuf [4]byte
	n, err := io.ReadFull(r.br, lenBuf[:])
	switch {
	case errors.Is(err, io.EOF) && n == 0:
		return Record{}, io.EOF
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return Record{}, fmt.Errorf("%w: truncated length prefix (%d bytes)", ErrCorruptRecord, n)
	case err != nil:
		return Record{}, fmt.Errorf("eventlog: read length: %w", err)
	}

	size := binary.BigEndian.Uint32(lenBuf[:])
	if size == 0 || size > MaxRecordSize {
		return Record{}, fmt.Errorf("%w: implausible record size %d", ErrCorruptRecord, size)
	}

	body := make([]byte, size)
	if _, err := io.ReadFull(r.br, body); err != nil {
		return Record{}, fmt.Errorf("%w: truncated body: %v", ErrCorruptRecord, err)
	}

	var rec Record
	if err := json.Unmarshal(body, &rec); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	return rec, nil
}
