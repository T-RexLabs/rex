package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// driveOnce pumps one request line through srv.Serve in a
// goroutine and reads back one JSON-RPC reply line. The pipe
// closes when the request is consumed so Serve returns rather
// than blocking forever waiting for more input.
func driveOnce(t *testing.T, srv *Server, request string) string {
	t.Helper()

	in := bytes.NewBufferString(request + "\n")
	out := &lineRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, in, out)
	}()

	select {
	case <-time.After(time.Second):
		t.Fatalf("timed out reading reply; out=%q", out.String())
	case <-done:
	}
	return out.String()
}

// driveSequence sends several requests in order and returns
// the concatenated output (one line per reply).
func driveSequence(t *testing.T, srv *Server, requests []string) string {
	t.Helper()
	var buf bytes.Buffer
	for _, r := range requests {
		buf.WriteString(r)
		buf.WriteString("\n")
	}
	out := &lineRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, &buf, out)
	}()
	select {
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out; out=%q", out.String())
	case <-done:
	}
	return out.String()
}

// lineRecorder is a thread-safe write sink — the server's
// json.Encoder may write concurrently with the test reading.
type lineRecorder struct {
	bytes.Buffer
}

func (r *lineRecorder) Write(p []byte) (int, error) { return r.Buffer.Write(p) }
func (r *lineRecorder) String() string              { return r.Buffer.String() }

// TestServerInitialize covers the protocol handshake: the
// server must announce its protocol version + tools capability
// so the harness knows what to ask for next.
func TestServerInitialize(t *testing.T) {
	t.Parallel()

	srv := New(ServerInfo{Name: "rex", Version: "test"})
	out := driveOnce(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if !strings.Contains(out, `"protocolVersion":"`+ProtocolVersion+`"`) {
		t.Fatalf("missing protocol version: %s", out)
	}
	if !strings.Contains(out, `"name":"rex"`) {
		t.Fatalf("missing server name: %s", out)
	}
	if !strings.Contains(out, `"tools":{"listChanged":false}`) {
		t.Fatalf("missing tools capability: %s", out)
	}
}

// TestServerToolsList confirms registered tools surface in
// tools/list with their descriptions + schemas.
func TestServerToolsList(t *testing.T) {
	t.Parallel()

	srv := New(ServerInfo{Name: "rex", Version: "test"})
	srv.Register(Tool{
		Name:        "demo.echo",
		Description: "Echo back the input.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}, func(_ context.Context, args json.RawMessage) (Result, error) {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(args, &p)
		return TextResult(p.Text), nil
	})

	out := driveOnce(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	for _, want := range []string{
		`"name":"demo.echo"`,
		`"description":"Echo back the input."`,
		`"inputSchema":`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in tools/list reply:\n%s", want, out)
		}
	}
}

// TestServerToolsCallSuccess routes an arguments payload to a
// handler and confirms the text content lands in the reply.
func TestServerToolsCallSuccess(t *testing.T) {
	t.Parallel()

	srv := New(ServerInfo{Name: "rex", Version: "test"})
	srv.Register(Tool{
		Name:        "demo.echo",
		Description: "Echo.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, args json.RawMessage) (Result, error) {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(args, &p)
		return TextResult("got: " + p.Text), nil
	})

	out := driveOnce(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo.echo","arguments":{"text":"hello"}}}`)
	if !strings.Contains(out, `"text":"got: hello"`) {
		t.Fatalf("missing echo result: %s", out)
	}
	if strings.Contains(out, `"isError":true`) {
		t.Fatalf("call should not have errored: %s", out)
	}
}

// TestServerToolsCallReportsError confirms a handler-returned
// error becomes an isError result rather than an RPC-level
// error, matching the MCP convention.
func TestServerToolsCallReportsError(t *testing.T) {
	t.Parallel()

	srv := New(ServerInfo{Name: "rex", Version: "test"})
	srv.Register(Tool{Name: "fails", Description: "x", InputSchema: json.RawMessage(`{}`)},
		func(_ context.Context, _ json.RawMessage) (Result, error) {
			return Result{}, io.ErrUnexpectedEOF
		})
	out := driveOnce(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fails"}}`)
	if !strings.Contains(out, `"isError":true`) {
		t.Fatalf("expected isError: %s", out)
	}
	if !strings.Contains(out, "unexpected EOF") {
		t.Fatalf("expected error text: %s", out)
	}
}

// TestServerHandlesUnknownMethod returns the JSON-RPC
// "method not found" code (-32601) so misbehaving harnesses
// see a structured error rather than a hang.
func TestServerHandlesUnknownMethod(t *testing.T) {
	t.Parallel()

	srv := New(ServerInfo{Name: "rex", Version: "test"})
	out := driveOnce(t, srv, `{"jsonrpc":"2.0","id":5,"method":"resources/list","params":{}}`)
	if !strings.Contains(out, `"code":-32601`) {
		t.Fatalf("expected method-not-found: %s", out)
	}
}

// TestServerSequencedRequests covers the multi-request flow a
// real harness drives: initialize, tools/list, tools/call, all
// on one stdio session.
func TestServerSequencedRequests(t *testing.T) {
	t.Parallel()

	srv := New(ServerInfo{Name: "rex", Version: "test"})
	srv.Register(Tool{Name: "demo.ping", Description: "x", InputSchema: json.RawMessage(`{}`)},
		func(_ context.Context, _ json.RawMessage) (Result, error) {
			return TextResult("pong"), nil
		})
	out := driveSequence(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo.ping"}}`,
	})
	for _, want := range []string{
		`"id":1`,
		`"id":2`,
		`"id":3`,
		`"protocolVersion"`,
		`"name":"demo.ping"`,
		`"text":"pong"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in combined output:\n%s", want, out)
		}
	}
}
