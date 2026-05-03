package sync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// Client speaks the sync.API surface to one central node.
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient returns a Client targeting baseURL (e.g.
// "http://127.0.0.1:8080"). The default http.Client uses a 30s
// per-request timeout; pass WithHTTPClient to override.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

// WithHTTPClient swaps the underlying *http.Client. Useful for tests
// that need a longer timeout or a custom transport.
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	if hc != nil {
		c.hc = hc
	}
	return c
}

// State fetches the central node's current state (head, fingerprint,
// protocol version).
func (c *Client) State(ctx context.Context) (proto.StateResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/sync/state", nil)
	if err != nil {
		return proto.StateResponse{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return proto.StateResponse{}, fmt.Errorf("sync: GET /sync/state: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proto.StateResponse{}, decodeError(resp)
	}
	var state proto.StateResponse
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return proto.StateResponse{}, fmt.Errorf("sync: decode state: %w", err)
	}
	return state, nil
}

// PushResult is the typed outcome of a successful push.
type PushResult struct {
	HeadID     string
	Accepted   int
	Duplicates int
}

// ConflictError is returned by Push when the server's head no longer
// matches the supplied since cursor. The DivergingTail field carries
// what the server has past the client's cursor; the rebase engine
// (sync.GIT.*) consumes it once it lands.
type ConflictError struct {
	ServerHead    string
	DivergingTail []eventlog.Record
}

// Error formats a short summary; full structured access is via the
// fields.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("sync: server head is %q; %d events to rebase", e.ServerHead, len(e.DivergingTail))
}

// IsConflict reports whether err is a *ConflictError.
func IsConflict(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}

// Push sends events past since to the server. Returns a typed
// PushResult on 200, *ConflictError on 409, or a generic error on
// other failure paths.
func (c *Client) Push(ctx context.Context, since string, events []eventlog.Record) (PushResult, error) {
	body, err := json.Marshal(proto.PushRequest{Since: since, Events: events})
	if err != nil {
		return PushResult{}, fmt.Errorf("sync: marshal push: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sync/events", bytes.NewReader(body))
	if err != nil {
		return PushResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return PushResult{}, fmt.Errorf("sync: POST /sync/events: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var pr proto.PushResponse
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			return PushResult{}, fmt.Errorf("sync: decode push response: %w", err)
		}
		return PushResult{HeadID: pr.HeadID, Accepted: pr.Accepted, Duplicates: pr.Duplicates}, nil
	case http.StatusConflict:
		var cr proto.ConflictResponse
		if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
			return PushResult{}, fmt.Errorf("sync: decode conflict: %w", err)
		}
		return PushResult{}, &ConflictError{ServerHead: cr.ServerHead, DivergingTail: cr.DivergingTail}
	default:
		return PushResult{}, decodeError(resp)
	}
}

// Pull streams events past since into the supplied callback in
// arrival order. The callback must return quickly; long work should
// happen after Pull returns. Returns the number of records observed.
func (c *Client) Pull(ctx context.Context, since string, fn func(eventlog.Record) error) (int, error) {
	q := ""
	if since != "" {
		q = "?since=" + since
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/sync/events"+q, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sync: GET /sync/events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, decodeError(resp)
	}
	count, err := scanSSE(resp.Body, fn)
	if err != nil {
		return count, err
	}
	return count, nil
}

// SyncResult is the combined outcome of Sync (Pull then Push).
type SyncResult struct {
	Pulled int
	Push   PushResult
}

// Sync pushes local events past the watermark, then pulls anything
// new from the server, then saves the advanced watermark
// (sync.CLIENT.3).
//
// The push-first ordering is what makes the algorithm correct
// against a single non-empty side: a fresh local syncing into a
// non-empty server skips the push (no local events past empty
// watermark) and pulls everything; a non-empty local syncing into a
// fresh server pushes everything; both-sides-non-empty diverges and
// returns *ConflictError, which the rebase engine (sync.GIT.*)
// consumes once it lands.
func (c *Client) Sync(ctx context.Context, workspaceRoot, remote string, eventsLogPath string) (SyncResult, error) {
	wm, err := LoadWatermark(workspaceRoot, remote)
	if err != nil {
		return SyncResult{}, err
	}

	var push PushResult
	tail, err := readEventsAfter(eventsLogPath, wm.LastAckedEventID)
	if err != nil {
		return SyncResult{}, err
	}
	if len(tail) > 0 {
		push, err = c.Push(ctx, wm.LastAckedEventID, tail)
		if err != nil {
			return SyncResult{}, err
		}
		wm.LastAckedEventID = push.HeadID
	}

	logWriter, err := openAppend(eventsLogPath)
	if err != nil {
		return SyncResult{Push: push}, err
	}
	defer logWriter.Close()
	var newHead string
	pulled, err := c.Pull(ctx, wm.LastAckedEventID, func(rec eventlog.Record) error {
		if err := appendRaw(logWriter, rec); err != nil {
			return err
		}
		newHead = rec.ID
		return nil
	})
	if err != nil {
		return SyncResult{Push: push, Pulled: pulled}, err
	}
	if newHead != "" {
		wm.LastAckedEventID = newHead
	}

	wm.AckedAt = time.Now().UTC()
	if err := SaveWatermark(workspaceRoot, wm); err != nil {
		return SyncResult{Pulled: pulled, Push: push}, err
	}
	return SyncResult{Pulled: pulled, Push: push}, nil
}

// scanSSE parses Server-Sent Events emitted by /sync/events GET and
// invokes fn for each `data: <SSEFrame>` line. Returns when the
// connection closes or an `: end` comment is observed.
func scanSSE(body io.Reader, fn func(eventlog.Record) error) (int, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			// SSE comment. The server marks end-of-stream with
			// `: end`; everything else is informational and
			// ignored.
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame proto.SSEFrame
		if err := json.Unmarshal([]byte(line[len("data: "):]), &frame); err != nil {
			return count, fmt.Errorf("sync: decode SSE frame: %w", err)
		}
		if err := fn(frame.Record); err != nil {
			return count, err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("sync: read SSE: %w", err)
	}
	return count, nil
}

// decodeError best-effort parses the standard ErrorResponse body
// and falls back to status text when it cannot.
func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var er proto.ErrorResponse
	if err := json.Unmarshal(body, &er); err == nil && er.Code != "" {
		return fmt.Errorf("sync: %s (%s): %s", resp.Status, er.Code, er.Message)
	}
	return fmt.Errorf("sync: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
