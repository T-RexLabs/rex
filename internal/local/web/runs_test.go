package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// seedRunEvents writes a synthetic run.* event sequence to
// .rex/events.log via the eventlog.Writer. Use it to populate a
// run's history without invoking the runner end-to-end.
func seedRunEvents(t *testing.T, root, runID string) {
	t.Helper()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(root, ".rex", "events.log"),
		WorkspaceID: "test-ws",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, ev := range []struct {
		typ     string
		payload string
	}{
		{"run.started", `{"run_id":"` + runID + `","started_at":"` + now + `"}`},
		{"node.started", `{"run_id":"` + runID + `","node_id":"shell","attempt":1,"started_at":"` + now + `"}`},
		{"node.succeeded", `{"run_id":"` + runID + `","node_id":"shell","attempt":1,"completed_at":"` + now + `"}`},
		{"run.completed", `{"run_id":"` + runID + `","completed_at":"` + now + `"}`},
	} {
		if _, err := w.Append(ev.typ, 1, json.RawMessage(ev.payload)); err != nil {
			t.Fatalf("Append %s: %v", ev.typ, err)
		}
	}
}

func TestRunsListEmpty(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-runs-empty")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/runs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "no runs yet") {
		t.Fatalf("expected empty hint: %s", body)
	}
}

func TestRunsListPopulated(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-runs-list")
	seedRunEvents(t, root, "alpha-run")
	seedRunEvents(t, root, "beta-run")

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/runs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"alpha-run", "beta-run", "completed"} {
		if !strings.Contains(body, want) {
			t.Errorf("/runs missing %q\n%s", want, body[:minInt(len(body), 1500)])
		}
	}
}

func TestRunDetailRendersHistory(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-detail")
	seedRunEvents(t, root, "the-run")

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/runs/the-run")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	for _, want := range []string{
		"run <code>the-run</code>",
		"run.started",
		"node.started",
		"node.succeeded",
		"run.completed",
		`pill-completed`,
		`sse-connect="/runs/the-run/stream"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q\n%s", want, body[:minInt(len(body), 2500)])
		}
	}
}

func TestRunDetailUnknownIDReturns404(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-404")
	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/runs/ghost")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRunStreamReplaysPriorEventsThenStays(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-stream")
	seedRunEvents(t, root, "stream-run")
	hs := newTestServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/runs/stream-run/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type: %q", resp.Header.Get("Content-Type"))
	}

	// Read until the context expires; we expect at least the four
	// seeded events to come through as `event: run-event` frames.
	buf := make([]byte, 64*1024)
	var collected strings.Builder
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	body := collected.String()
	frames := strings.Count(body, "event: run-event")
	if frames < 4 {
		t.Fatalf("expected at least 4 run-event frames, got %d:\n%s", frames, body)
	}
	for _, want := range []string{"run.started", "node.started", "node.succeeded", "run.completed"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q", want)
		}
	}
}

func TestRunStreamPicksUpAppendedEvent(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-stream-tail")
	hs := newTestServer(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// Open the stream BEFORE writing any events.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/runs/tail-run/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	// Now append events out-of-band via the eventlog Writer. The
	// SSE handler is polling and should pick them up within a
	// couple of poll intervals.
	go func() {
		time.Sleep(150 * time.Millisecond)
		seedRunEvents(t, root, "tail-run")
	}()

	buf := make([]byte, 64*1024)
	var collected strings.Builder
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	body := collected.String()
	if !strings.Contains(body, "tail-run") {
		t.Fatalf("stream did not pick up tail-appended events:\n%s", body)
	}
	if !strings.Contains(body, "run.completed") {
		t.Fatalf("stream missing terminal event:\n%s", body)
	}
}
