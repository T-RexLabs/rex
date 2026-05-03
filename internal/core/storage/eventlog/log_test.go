package eventlog

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	clock := NewClockWithSource(stepClock(time.Unix(1700000000, 0)))
	w, err := OpenWriter(WriterConfig{
		Path:        path,
		WorkspaceID: "ws-test",
		Actor:       "fp-test",
		Clock:       clock,
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

// stepClock returns a clock source that advances by one nanosecond on
// each call, starting from base. Lets append tests get distinct HLCs
// without relying on the real wall clock (overview.ENG.4).
func stepClock(base time.Time) func() time.Time {
	var (
		mu sync.Mutex
		n  int64
	)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := base.Add(time.Duration(n))
		n++
		return t
	}
}

func TestWriterAppendStampsMetadata(t *testing.T) {
	t.Parallel()

	w, path := newTestWriter(t)

	rec, err := w.Append("hello", 1, json.RawMessage(`{"msg":"world"}`))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rec.WorkspaceID != "ws-test" || rec.Actor != "fp-test" {
		t.Fatalf("metadata not stamped: %+v", rec)
	}
	if rec.ID == "" || rec.Timestamp.Wall == 0 {
		t.Fatalf("HLC not assigned: %+v", rec)
	}
	if rec.ID != rec.Timestamp.String() {
		t.Fatalf("ID should be HLC string: id=%q timestamp=%q", rec.ID, rec.Timestamp.String())
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("events.log should not be empty after Append")
	}
}

func TestReaderReplaysWrittenRecords(t *testing.T) {
	t.Parallel()

	w, path := newTestWriter(t)

	want := []json.RawMessage{
		json.RawMessage(`{"i":1}`),
		json.RawMessage(`{"i":2}`),
		json.RawMessage(`{"i":3}`),
	}
	for i, p := range want {
		if _, err := w.Append("step", uint32(i), p); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	for i := 0; ; i++ {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			if i != len(want) {
				t.Fatalf("read %d records, want %d", i, len(want))
			}
			return
		}
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		if rec.Type != "step" {
			t.Fatalf("record %d type: got %q", i, rec.Type)
		}
		if string(rec.Payload) != string(want[i]) {
			t.Fatalf("record %d payload: got %s want %s", i, rec.Payload, want[i])
		}
		if uint32(i) != rec.Version {
			t.Fatalf("record %d version: got %d want %d", i, rec.Version, i)
		}
	}
}

func TestReaderEOFOnEmptyLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty log: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next on empty log: got %v want io.EOF", err)
	}
}

func TestReaderRejectsTruncatedRecord(t *testing.T) {
	t.Parallel()

	w, path := newTestWriter(t)
	if _, err := w.Append("hello", 1, json.RawMessage(`{"msg":"world"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := os.Truncate(path, st.Size()-3); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	_, err = r.Next()
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("Next on truncated: got %v want ErrCorruptRecord", err)
	}
}

func TestReaderRejectsImplausibleSize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], MaxRecordSize+1)
	if err := os.WriteFile(path, buf[:], 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	_, err = r.Next()
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("Next on implausible size: got %v want ErrCorruptRecord", err)
	}
}

func TestWriterConcurrentAppends(t *testing.T) {
	t.Parallel()

	w, path := newTestWriter(t)

	const goroutines = 8
	const perGoroutine = 25

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				payload := json.RawMessage([]byte(`{"g":` + itoa(g) + `,"i":` + itoa(i) + `}`))
				if _, err := w.Append("concurrent", 1, payload); err != nil {
					t.Errorf("Append g=%d i=%d: %v", g, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	count := 0
	var prev HLC
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next at count=%d: %v", count, err)
		}
		if count > 0 && !prev.Less(rec.Timestamp) {
			t.Fatalf("HLCs not strictly increasing at count=%d: prev=%+v cur=%+v", count, prev, rec.Timestamp)
		}
		prev = rec.Timestamp
		count++
	}
	if want := goroutines * perGoroutine; count != want {
		t.Fatalf("read %d records, want %d", count, want)
	}
}

func TestOpenWriterValidatesConfig(t *testing.T) {
	t.Parallel()

	if _, err := OpenWriter(WriterConfig{}); err == nil {
		t.Fatal("missing path: want error")
	}
	if _, err := OpenWriter(WriterConfig{Path: filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Fatal("missing workspace: want error")
	}
}

// itoa is a tiny stdlib-free integer formatter so the concurrent test
// can avoid importing strconv just to assemble payload bytes.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
