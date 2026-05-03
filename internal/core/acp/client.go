package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// FrameObserver is invoked for every frame the client receives, BEFORE
// any routing or callback dispatch. Per execution.ACP.3 every received
// ACP frame must be captured to the run transcript before further
// processing — this hook is how the executor gets that capture for
// free, given the raw bytes already preserved by the framing layer.
type FrameObserver func(RawMessage)

// Config drives a Client.
type Config struct {
	// Recv carries frames from the harness. EOF here ends the read
	// loop and causes pending Calls to return ErrClientClosed.
	Recv io.Reader
	// Send accepts frames going to the harness.
	Send io.Writer
	// OnFrame is the transcript-capture hook (execution.ACP.3). It
	// runs synchronously on the read goroutine, so it must be cheap;
	// anything slower than an event-log Append belongs in a buffered
	// downstream consumer.
	OnFrame FrameObserver
	// OnPermission resolves session/request_permission frames
	// (execution.ACP.4). If nil, every permission request is denied
	// with "no permission handler installed".
	OnPermission PermissionHandler
	// Close is invoked by Client.Close after the read loop exits. A
	// process-spawning helper sets this to terminate the harness.
	Close func() error
}

// ErrClientClosed is returned by Call/Notify when the read loop has
// ended (EOF, transport error, or an explicit Close).
var ErrClientClosed = errors.New("acp: client closed")

// Client speaks ACP to a harness over the configured Recv/Send pair. A
// Client is safe for concurrent use.
type Client struct {
	reader *Reader
	writer *Writer

	onFrame      FrameObserver
	onPermission PermissionHandler

	closeFn func() error

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan Message

	closeOnce sync.Once
	runDone   chan struct{}
	runErrMu  sync.Mutex
	runErr    error
}

// New starts a Client and its read loop.
func New(cfg Config) (*Client, error) {
	if cfg.Recv == nil {
		return nil, errors.New("acp: Config.Recv is required")
	}
	if cfg.Send == nil {
		return nil, errors.New("acp: Config.Send is required")
	}
	c := &Client{
		reader:       NewReader(cfg.Recv),
		writer:       NewWriter(cfg.Send),
		onFrame:      cfg.OnFrame,
		onPermission: cfg.OnPermission,
		closeFn:      cfg.Close,
		pending:      make(map[string]chan Message),
		runDone:      make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Close ends the client. The read loop already drained on EOF will
// have closed runDone; explicit callers wait for that here. If a
// closeFn was configured (e.g. closing stdin to signal the harness),
// it runs first.
func (c *Client) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.closeFn != nil {
			closeErr = c.closeFn()
		}
	})
	<-c.runDone
	if closeErr != nil {
		return closeErr
	}
	return c.readErr()
}

// Done returns a channel that is closed when the read loop ends.
func (c *Client) Done() <-chan struct{} { return c.runDone }

// Err returns the read-loop's terminal error, if any. Returns nil if
// the loop has not exited or exited via clean EOF.
func (c *Client) Err() error { return c.readErr() }

func (c *Client) readErr() error {
	c.runErrMu.Lock()
	defer c.runErrMu.Unlock()
	return c.runErr
}

func (c *Client) setReadErr(err error) {
	c.runErrMu.Lock()
	defer c.runErrMu.Unlock()
	if c.runErr == nil {
		c.runErr = err
	}
}

// Call issues a JSON-RPC request and blocks until the response arrives,
// the read loop ends, or ctx is cancelled.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	msg, err := NewRequest(id, method, params)
	if err != nil {
		return nil, err
	}

	ch := make(chan Message, 1)
	key := string(msg.ID)
	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
	}()

	if err := c.writer.Write(msg); err != nil {
		return nil, fmt.Errorf("acp: write request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.runDone:
		if e := c.readErr(); e != nil {
			return nil, e
		}
		return nil, ErrClientClosed
	}
}

// Notify sends a notification (no id, no expected response).
func (c *Client) Notify(method string, params any) error {
	msg, err := NewNotification(method, params)
	if err != nil {
		return err
	}
	return c.writer.Write(msg)
}

func (c *Client) readLoop() {
	defer close(c.runDone)

	for {
		raw, err := c.reader.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			c.setReadErr(err)
			return
		}

		// Capture FIRST, route SECOND — execution.ACP.3.
		if c.onFrame != nil {
			c.onFrame(raw)
		}

		msg := raw.Message
		switch {
		case msg.IsResponse():
			c.deliverResponse(msg)
		case msg.IsRequest():
			go c.handleIncomingRequest(msg)
		case msg.IsNotification():
			// Notifications are observed via OnFrame only in v1.
		}
	}
}

func (c *Client) deliverResponse(msg Message) {
	key := string(msg.ID)
	c.pendingMu.Lock()
	ch, ok := c.pending[key]
	c.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
		// Channel buffered to 1; a duplicate response is impossible
		// for a well-behaved peer, and dropping a duplicate is the
		// least-surprising thing to do.
	}
}

func (c *Client) handleIncomingRequest(req Message) {
	switch req.Method {
	case MethodSessionRequestPermission:
		c.handlePermission(req)
	default:
		c.replyError(req.ID, ErrCodeMethodNotFound, "unhandled method: "+req.Method)
	}
}

func (c *Client) handlePermission(req Message) {
	var pr PermissionRequest
	if err := json.Unmarshal(req.Params, &pr); err != nil {
		c.replyError(req.ID, ErrCodeInvalidParams, err.Error())
		return
	}

	if c.onPermission == nil {
		c.replyResult(req.ID, PermissionDecision{
			Granted: false,
			Note:    "rex: no permission handler installed",
		})
		return
	}

	decision, err := c.onPermission(context.Background(), pr)
	if err != nil {
		c.replyError(req.ID, ErrCodeInternalError, err.Error())
		return
	}
	c.replyResult(req.ID, decision)
}

func (c *Client) replyResult(id json.RawMessage, result any) {
	resp, err := NewResponse(id, result)
	if err != nil {
		// Should never happen for the simple results we emit here,
		// but if it does fall back to a generic error reply.
		c.replyError(id, ErrCodeInternalError, "rex: build response: "+err.Error())
		return
	}
	_ = c.writer.Write(resp)
}

func (c *Client) replyError(id json.RawMessage, code int, message string) {
	resp, err := NewErrorResponse(id, code, message, nil)
	if err != nil {
		// Cannot recover further; drop.
		return
	}
	_ = c.writer.Write(resp)
}
