package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestReaderParsesMultipleFrames(t *testing.T) {
	t.Parallel()

	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"a":1}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"chunk":"hello"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"sess-1"}}`,
		"",
	}, "\n")

	r := NewReader(strings.NewReader(stream))

	first, err := r.Next()
	if err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	if !first.Message.IsRequest() || first.Message.Method != "session/new" {
		t.Fatalf("frame 1 classification: %+v", first.Message)
	}

	second, err := r.Next()
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if !second.Message.IsNotification() {
		t.Fatalf("frame 2 not a notification: %+v", second.Message)
	}

	third, err := r.Next()
	if err != nil {
		t.Fatalf("frame 3: %v", err)
	}
	if !third.Message.IsResponse() {
		t.Fatalf("frame 3 not a response: %+v", third.Message)
	}

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReaderPreservesRawBytes(t *testing.T) {
	t.Parallel()

	frame := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"workspaceId":"ws"}}`
	r := NewReader(strings.NewReader(frame + "\n"))

	got, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(got.Raw) != frame {
		t.Fatalf("raw mismatch:\n got %q\nwant %q", got.Raw, frame)
	}
}

func TestReaderAcceptsCRLF(t *testing.T) {
	t.Parallel()

	frame := `{"jsonrpc":"2.0","id":1,"method":"x"}`
	r := NewReader(strings.NewReader(frame + "\r\n"))

	got, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(got.Raw) != frame {
		t.Fatalf("raw mismatch:\n got %q\nwant %q", got.Raw, frame)
	}
}

func TestReaderRejectsTornFrame(t *testing.T) {
	t.Parallel()

	r := NewReader(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"x"`)) // no newline
	_, err := r.Next()
	if err == nil {
		t.Fatal("Next: want error on torn frame")
	}
	if !strings.Contains(err.Error(), "torn frame") {
		t.Fatalf("error should mention torn frame: %v", err)
	}
}

func TestReaderEOFOnEmpty(t *testing.T) {
	t.Parallel()

	r := NewReader(strings.NewReader(""))
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next on empty: got %v want io.EOF", err)
	}
}

func TestReaderRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	r := NewReader(strings.NewReader("not-json\n"))
	_, err := r.Next()
	if err == nil {
		t.Fatal("Next on invalid JSON: want error")
	}
	if !strings.Contains(err.Error(), "decode frame") {
		t.Fatalf("error should mention decode: %v", err)
	}
}

func TestWriterEmitsNDJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := NewWriter(&buf)

	msg, err := NewRequest(1, "session/new", map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := w.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("Writer did not append newline: %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("Writer should emit exactly one newline: %q", out)
	}

	r := NewReader(strings.NewReader(out))
	got, err := r.Next()
	if err != nil {
		t.Fatalf("round-trip Next: %v", err)
	}
	if !got.Message.IsRequest() || got.Message.Method != "session/new" {
		t.Fatalf("round-trip classification: %+v", got.Message)
	}
}

func TestWriterConcurrent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := NewWriter(&buf)

	const goroutines = 10
	const perGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				msg, err := NewNotification("session/update", map[string]int{"g": g, "i": i})
				if err != nil {
					t.Errorf("NewNotification: %v", err)
					return
				}
				if err := w.Write(msg); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	r := NewReader(&buf)
	count := 0
	for {
		_, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next at count=%d: %v", count, err)
		}
		count++
	}
	if want := goroutines * perGoroutine; count != want {
		t.Fatalf("read %d frames, want %d", count, want)
	}
}

// TestReaderHandlesLargeFrame verifies the multi-chunk read path
// assembles frames larger than the bufio default buffer (4 KiB).
func TestReaderHandlesLargeFrame(t *testing.T) {
	t.Parallel()

	bigParam := strings.Repeat("a", 32*1024)
	body := map[string]string{"data": bigParam}
	msg, err := NewRequest(1, "session/prompt", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	r := NewReader(bytes.NewReader(append(encoded, '\n')))
	got, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(got.Message.Params, &decoded); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if decoded["data"] != bigParam {
		t.Fatalf("large frame round-trip lost data: got %d bytes want %d", len(decoded["data"]), len(bigParam))
	}
}
