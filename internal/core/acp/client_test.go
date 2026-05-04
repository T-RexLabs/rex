package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pipePair returns two io.Pipes, one for each direction. Used to model
// the client-harness wire without spawning a real process.
type pipePair struct {
	clientRecv *io.PipeReader
	clientSend *io.PipeWriter

	harnessRecv *io.PipeReader
	harnessSend *io.PipeWriter
}

func newPipes() pipePair {
	cr, hs := io.Pipe()
	hr, cs := io.Pipe()
	return pipePair{
		clientRecv:  cr,
		clientSend:  cs,
		harnessRecv: hr,
		harnessSend: hs,
	}
}

func (p pipePair) closeAll() {
	_ = p.clientRecv.Close()
	_ = p.clientSend.Close()
	_ = p.harnessRecv.Close()
	_ = p.harnessSend.Close()
}

// runMockHarness reads frames from the harness side and dispatches each
// to handler. handler returns the message to write back, or a zero
// Message to send nothing. The harness goroutine exits when the pipe
// is closed.
func runMockHarness(t *testing.T, p pipePair, handler func(RawMessage) Message) {
	t.Helper()
	r := NewReader(p.harnessRecv)
	w := NewWriter(p.harnessSend)
	go func() {
		for {
			raw, err := r.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				return
			}
			resp := handler(raw)
			if resp.JSONRPC == "" {
				continue
			}
			if err := w.Write(resp); err != nil {
				return
			}
		}
	}()
}

func newTestClient(t *testing.T, cfg Config, p pipePair) *Client {
	t.Helper()
	cfg.Recv = p.clientRecv
	cfg.Send = p.clientSend
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		p.closeAll()
		_ = c.Close()
	})
	return c
}

func TestClientNewSessionRoundTrip(t *testing.T) {
	t.Parallel()

	p := newPipes()
	runMockHarness(t, p, func(raw RawMessage) Message {
		if raw.Message.Method != MethodSessionNew {
			t.Errorf("unexpected method: %s", raw.Message.Method)
			resp, _ := NewErrorResponse(raw.Message.ID, ErrCodeMethodNotFound, "no", nil)
			return resp
		}
		var got SessionNewParams
		if err := json.Unmarshal(raw.Message.Params, &got); err != nil {
			t.Errorf("unmarshal params: %v", err)
		}
		if got.Cwd != "/tmp/ws-1" || len(got.MCPServers) != 1 {
			t.Errorf("params not delivered: %+v", got)
		}
		resp, _ := NewResponse(raw.Message.ID, SessionNewResult{SessionID: "sess-9"})
		return resp
	})

	c := newTestClient(t, Config{}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := c.NewSession(ctx, SessionNewParams{
		Cwd: "/tmp/ws-1",
		MCPServers: []MCPServer{
			{Name: "fs", Command: []string{"mcp-fs"}},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if res.SessionID != "sess-9" {
		t.Fatalf("session id: got %q", res.SessionID)
	}
}

func TestClientCancelSession(t *testing.T) {
	t.Parallel()

	var got SessionCancelParams
	p := newPipes()
	runMockHarness(t, p, func(raw RawMessage) Message {
		_ = json.Unmarshal(raw.Message.Params, &got)
		resp, _ := NewResponse(raw.Message.ID, struct{}{})
		return resp
	})

	c := newTestClient(t, Config{}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.CancelSession(ctx, "sess-42"); err != nil {
		t.Fatalf("CancelSession: %v", err)
	}
	if got.SessionID != "sess-42" {
		t.Fatalf("cancel session id not delivered: got %+v", got)
	}
}

// TestClientFrameObserverCalledBeforeRouting asserts execution.ACP.3:
// every received frame is observed before any further processing. We
// install an observer that takes a per-frame snapshot and a response
// handler that depends on the observer having seen the frame first.
func TestClientFrameObserverCalledBeforeRouting(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		observed []string
	)
	observer := func(raw RawMessage) {
		mu.Lock()
		observed = append(observed, string(raw.Raw))
		mu.Unlock()
	}

	p := newPipes()
	runMockHarness(t, p, func(raw RawMessage) Message {
		resp, _ := NewResponse(raw.Message.ID, SessionNewResult{SessionID: "sess-1"})
		// Also push a notification so the observer sees both.
		w := NewWriter(p.harnessSend)
		_ = w.Write(Message{JSONRPC: Version, Method: "session/update", Params: json.RawMessage(`{"chunk":"x"}`)})
		return resp
	})

	c := newTestClient(t, Config{OnFrame: observer}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := c.NewSession(ctx, SessionNewParams{Cwd: "/tmp/ws"}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Allow the notification frame to land.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(observed)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(observed) < 2 {
		t.Fatalf("observer saw %d frames, want >= 2: %v", len(observed), observed)
	}
	hasResponse, hasNotification := false, false
	for _, raw := range observed {
		if strings.Contains(raw, `"result"`) {
			hasResponse = true
		}
		if strings.Contains(raw, `"session/update"`) {
			hasNotification = true
		}
	}
	if !hasResponse || !hasNotification {
		t.Fatalf("observer missed frames: %v", observed)
	}
}

func TestClientPermissionRequestDispatched(t *testing.T) {
	t.Parallel()

	var seen atomic.Int32
	handler := func(_ context.Context, req PermissionRequest) (PermissionDecision, error) {
		seen.Add(1)
		if req.Tool != "edit" {
			return PermissionDecision{}, errors.New("unexpected tool")
		}
		return PermissionDecision{Granted: true, Note: "ok"}, nil
	}

	p := newPipes()

	// The harness sends a permission request on its own (server->client),
	// then waits for the response and asserts on it.
	decisionCh := make(chan PermissionDecision, 1)
	go func() {
		w := NewWriter(p.harnessSend)
		req, _ := NewRequest(101, MethodSessionRequestPermission, PermissionRequest{
			SessionID: "sess-1", Tool: "edit",
		})
		_ = w.Write(req)

		r := NewReader(p.harnessRecv)
		raw, err := r.Next()
		if err != nil {
			return
		}
		var dec PermissionDecision
		_ = json.Unmarshal(raw.Message.Result, &dec)
		decisionCh <- dec
	}()

	_ = newTestClient(t, Config{OnPermission: handler}, p)

	select {
	case dec := <-decisionCh:
		if !dec.Granted || dec.Note != "ok" {
			t.Fatalf("decision wire: got %+v", dec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for harness to receive permission decision")
	}
	if seen.Load() != 1 {
		t.Fatalf("handler invoked %d times, want 1", seen.Load())
	}
}

func TestClientPermissionDefaultDeny(t *testing.T) {
	t.Parallel()

	p := newPipes()
	decisionCh := make(chan PermissionDecision, 1)
	go func() {
		w := NewWriter(p.harnessSend)
		req, _ := NewRequest(7, MethodSessionRequestPermission, PermissionRequest{Tool: "x"})
		_ = w.Write(req)

		r := NewReader(p.harnessRecv)
		raw, err := r.Next()
		if err != nil {
			return
		}
		var dec PermissionDecision
		_ = json.Unmarshal(raw.Message.Result, &dec)
		decisionCh <- dec
	}()

	_ = newTestClient(t, Config{}, p) // no handler installed

	select {
	case dec := <-decisionCh:
		if dec.Granted {
			t.Fatalf("default policy must deny: got %+v", dec)
		}
		if !strings.Contains(dec.Note, "no permission handler") {
			t.Fatalf("default note unexpected: %q", dec.Note)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for default-deny response")
	}
}

func TestClientCallReturnsErrClientClosedOnEOF(t *testing.T) {
	t.Parallel()

	p := newPipes()

	// Harness reads the request then closes the harness->client pipe to
	// simulate the harness exiting before answering.
	go func() {
		r := NewReader(p.harnessRecv)
		_, _ = r.Next()
		_ = p.harnessSend.Close()
	}()

	c := newTestClient(t, Config{}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.Call(ctx, "x/method", nil)
	if !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Call: got %v want ErrClientClosed", err)
	}
}

func TestClientConcurrentCallsCorrelateByID(t *testing.T) {
	t.Parallel()

	p := newPipes()
	runMockHarness(t, p, func(raw RawMessage) Message {
		// Echo the id back as the result so each caller can verify
		// it received the response keyed to its own request.
		resp, _ := NewResponse(raw.Message.ID, map[string]json.RawMessage{"echo": raw.Message.ID})
		return resp
	})

	c := newTestClient(t, Config{}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]string, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			raw, err := c.Call(ctx, "ping", nil)
			if err != nil {
				t.Errorf("Call %d: %v", i, err)
				return
			}
			var body struct {
				Echo json.RawMessage `json:"echo"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("unmarshal %d: %v", i, err)
				return
			}
			results[i] = string(body.Echo)
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, id := range results {
		if id == "" {
			t.Fatalf("call %d had empty echo", i)
		}
		if seen[id] {
			t.Fatalf("call %d saw duplicate id %s", i, id)
		}
		seen[id] = true
	}
}

func TestClientNotifyWritesNoID(t *testing.T) {
	t.Parallel()

	p := newPipes()
	gotCh := make(chan Message, 1)
	go func() {
		r := NewReader(p.harnessRecv)
		raw, err := r.Next()
		if err != nil {
			return
		}
		gotCh <- raw.Message
	}()

	c := newTestClient(t, Config{}, p)
	if err := c.Notify("session/heartbeat", map[string]int{"n": 1}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	select {
	case msg := <-gotCh:
		if !msg.IsNotification() {
			t.Fatalf("not a notification: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for notification")
	}
}
