package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxFrameSize bounds a single NDJSON frame to keep a chatty or hostile
// peer from forcing an unbounded allocation. ACP frames are typically
// well under 1 MiB; 16 MiB matches storage.MaxRecordSize so a frame
// that fits over the wire also fits in the event log.
const MaxFrameSize = 16 * 1024 * 1024

// ErrFrameTooLarge is returned when a single frame exceeds MaxFrameSize.
var ErrFrameTooLarge = errors.New("acp: frame exceeds MaxFrameSize")

// RawMessage pairs a parsed JSON-RPC Message with the exact bytes it was
// decoded from. The raw bytes are what callers should append to the
// transcript per execution.ACP.3 — the parsed Message is for routing.
type RawMessage struct {
	Message Message
	Raw     []byte
}

// Reader streams NDJSON frames out of an underlying io.Reader. Each call
// to Next blocks until either a full line arrives, the stream ends, or
// the context is cancelled by closing the underlying reader. Reader is
// not safe for concurrent use.
type Reader struct {
	br *bufio.Reader
}

// NewReader wraps r as an NDJSON frame reader. The bufio buffer uses
// the package default (4 KiB); larger frames assemble across multiple
// reads in Next, capped by MaxFrameSize. We avoid pre-allocating
// MaxFrameSize per Reader because Rex may drive several harness
// sessions concurrently.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReader(r)}
}

// Next reads the next NDJSON frame and parses it. Returns io.EOF cleanly
// on stream end; ErrFrameTooLarge if a single frame exceeds the size
// budget; or a wrapped JSON error if the frame is not valid JSON-RPC.
func (r *Reader) Next() (RawMessage, error) {
	line, err := readNDJSONLine(r.br)
	if err != nil {
		return RawMessage{}, err
	}
	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return RawMessage{}, fmt.Errorf("acp: decode frame: %w", err)
	}
	return RawMessage{Message: msg, Raw: line}, nil
}

// readNDJSONLine reads one newline-terminated line, returning bytes
// without the trailing '\n'. It rejects frames larger than MaxFrameSize
// without consuming the rest of the offending line — at that point the
// stream's framing is unrecoverable and the caller must treat it as
// fatal anyway.
func readNDJSONLine(br *bufio.Reader) ([]byte, error) {
	var (
		line  []byte
		total int
	)
	for {
		chunk, err := br.ReadSlice('\n')
		total += len(chunk)
		if total > MaxFrameSize {
			return nil, ErrFrameTooLarge
		}
		if err == nil {
			// chunk ends in '\n'; strip it and any trailing '\r'.
			if len(line) == 0 {
				line = trimNewline(chunk)
				return cloneBytes(line), nil
			}
			line = append(line, trimNewline(chunk)...)
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, chunk...)
			continue
		}
		if errors.Is(err, io.EOF) {
			if total == 0 {
				return nil, io.EOF
			}
			// Trailing bytes without a newline are a torn frame.
			return nil, fmt.Errorf("acp: torn frame at EOF (%d bytes)", total)
		}
		return nil, fmt.Errorf("acp: read frame: %w", err)
	}
}

func trimNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// Writer serializes Messages back to NDJSON. A Writer is safe for
// concurrent use; concurrent Writes are serialized internally so a
// large frame is never interleaved with another.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter wraps w. Callers should ensure w is line-buffered or
// flushed externally — Writer issues one Write per frame and does not
// buffer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write encodes msg as a single NDJSON line and writes it. The encoding
// adds the trailing newline; the message body must not contain bare
// newlines (encoding/json never emits any).
func (w *Writer) Write(msg Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("acp: marshal frame: %w", err)
	}
	if len(body)+1 > MaxFrameSize {
		return ErrFrameTooLarge
	}
	frame := make([]byte, len(body)+1)
	copy(frame, body)
	frame[len(body)] = '\n'

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(frame); err != nil {
		return fmt.Errorf("acp: write frame: %w", err)
	}
	return nil
}
