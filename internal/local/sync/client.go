package sync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// Client speaks the sync.API surface to one central node.
type Client struct {
	baseURL string
	hc      *http.Client

	signer identity.Signer

	tokenMu     sync.Mutex
	accessToken string
	tokenExpiry time.Time
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

// WithSigner attaches a Signer the client uses to handshake against
// servers that require auth. When the signer is nil (default), the
// client never attempts a handshake and any 401 from the server
// surfaces as an error.
func (c *Client) WithSigner(s identity.Signer) *Client {
	c.signer = s
	return c
}

// authorize attaches the current access token to a request when
// available. Callers run handshake() first if no token is present
// and the server requires auth.
func (c *Client) authorize(req *http.Request) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
}

// handshake runs the challenge-response flow with the server. The
// returned access token is stored on the client and attached to
// subsequent requests via authorize.
func (c *Client) handshake(ctx context.Context) error {
	if c.signer == nil {
		return errors.New("sync: server requires auth but client has no Signer (call WithSigner)")
	}
	// 1. Fetch challenge.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth/challenge", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("sync: POST /auth/challenge: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeError(resp)
	}
	var ch proto.AuthChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return fmt.Errorf("sync: decode challenge: %w", err)
	}
	// 2. Sign canonical input.
	canonical, err := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    ch.Nonce,
		Hostname: ch.Hostname,
		Scope:    "sync",
	})
	if err != nil {
		return fmt.Errorf("sync: marshal signing input: %w", err)
	}
	sig, err := c.signer.Sign(ctx, canonical)
	if err != nil {
		return fmt.Errorf("sync: sign challenge: %w", err)
	}
	// 3. POST verify.
	verifyBody, err := json.Marshal(proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: c.signer.Fingerprint().String(),
		Scope:       "sync",
		Signature:   hex.EncodeToString(sig),
	})
	if err != nil {
		return fmt.Errorf("sync: marshal verify: %w", err)
	}
	vReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth/verify",
		bytes.NewReader(verifyBody))
	if err != nil {
		return err
	}
	vReq.Header.Set("Content-Type", "application/json")
	vResp, err := c.hc.Do(vReq)
	if err != nil {
		return fmt.Errorf("sync: POST /auth/verify: %w", err)
	}
	defer vResp.Body.Close()
	if vResp.StatusCode != http.StatusOK {
		return decodeError(vResp)
	}
	var verifyRes proto.AuthVerifyResponse
	if err := json.NewDecoder(vResp.Body).Decode(&verifyRes); err != nil {
		return fmt.Errorf("sync: decode verify: %w", err)
	}
	c.tokenMu.Lock()
	c.accessToken = verifyRes.AccessToken
	c.tokenExpiry = verifyRes.ExpiresAt
	c.tokenMu.Unlock()
	return nil
}

// doAuthorized issues req. If the server returns 401 and the
// client has a Signer, it runs a handshake and retries once.
// Mutating-body callers (Push) must reuse a fresh body on retry,
// so they pass a bodyFactory that returns one io.Reader per
// invocation.
func (c *Client) doAuthorized(ctx context.Context, method, url string, bodyFactory func() (io.Reader, error)) (*http.Response, error) {
	build := func() (*http.Request, error) {
		var body io.Reader
		if bodyFactory != nil {
			b, err := bodyFactory()
			if err != nil {
				return nil, err
			}
			body = b
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "text/event-stream")
		c.authorize(req)
		return req, nil
	}

	req, err := build()
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || c.signer == nil {
		return resp, nil
	}
	// Drain the 401 body so the connection is reusable.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := c.handshake(ctx); err != nil {
		return nil, err
	}
	retry, err := build()
	if err != nil {
		return nil, err
	}
	return c.hc.Do(retry)
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
// other failure paths. When the client has a Signer attached and
// the server returns 401, a handshake fires and the request is
// retried once.
func (c *Client) Push(ctx context.Context, since string, events []eventlog.Record) (PushResult, error) {
	body, err := json.Marshal(proto.PushRequest{Since: since, Events: events})
	if err != nil {
		return PushResult{}, fmt.Errorf("sync: marshal push: %w", err)
	}
	resp, err := c.doAuthorized(ctx, http.MethodPost, c.baseURL+"/sync/events", func() (io.Reader, error) {
		return bytes.NewReader(body), nil
	})
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
// Auto-retries once on 401 when the client has a Signer attached.
func (c *Client) Pull(ctx context.Context, since string, fn func(eventlog.Record) error) (int, error) {
	q := ""
	if since != "" {
		q = "?since=" + since
	}
	resp, err := c.doAuthorized(ctx, http.MethodGet, c.baseURL+"/sync/events"+q, nil)
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

// RunArgs is the per-call configuration for the half- and full-sync
// operations that wrap the watermark + log dance around HTTP calls.
type RunArgs struct {
	WorkspaceRoot string
	Remote        string
	EventsLogPath string
}

// PushOnly is the push half of Sync. Reads events past the watermark,
// pushes them, and on success advances the watermark and saves it.
// Returns a zero-Accepted PushResult when there is nothing to push,
// so callers can branch on Accepted == 0 without special-casing.
//
// On a *ConflictError the watermark is updated in place: NeedsRebase
// flips to true and LastConflictHead is stamped with the server head
// the conflict reported. The watermark file is saved so the rebase-
// needed signal is durable across CLI invocations (sync.DRAFT.2). The
// underlying *ConflictError is returned unchanged so callers can keep
// branching on errors.As as before.
func (c *Client) PushOnly(ctx context.Context, args RunArgs) (PushResult, error) {
	wm, err := LoadWatermark(args.WorkspaceRoot, args.Remote)
	if err != nil {
		return PushResult{}, err
	}
	tail, err := readEventsAfter(args.EventsLogPath, wm.LastAckedEventID)
	if err != nil {
		return PushResult{}, err
	}
	if len(tail) == 0 {
		return PushResult{HeadID: wm.LastAckedEventID}, nil
	}
	push, err := c.Push(ctx, wm.LastAckedEventID, tail)
	if err != nil {
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			wm.NeedsRebase = true
			wm.LastConflictHead = conflict.ServerHead
			// Best-effort persist: a save failure here does
			// not change the original error semantics — the
			// caller still sees the typed *ConflictError.
			_ = SaveWatermark(args.WorkspaceRoot, wm)
		}
		return PushResult{}, err
	}
	wm.LastAckedEventID = push.HeadID
	wm.AckedAt = time.Now().UTC()
	wm.NeedsRebase = false
	wm.LastConflictHead = ""
	if err := SaveWatermark(args.WorkspaceRoot, wm); err != nil {
		return push, err
	}
	return push, nil
}

// PullOnly is the pull half of Sync. Streams events past the
// watermark into the local events.log, advancing the watermark to
// the last received event id on success.
func (c *Client) PullOnly(ctx context.Context, args RunArgs) (int, error) {
	wm, err := LoadWatermark(args.WorkspaceRoot, args.Remote)
	if err != nil {
		return 0, err
	}
	logWriter, err := openAppend(args.EventsLogPath)
	if err != nil {
		return 0, err
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
		return pulled, err
	}
	if newHead != "" {
		wm.LastAckedEventID = newHead
		wm.AckedAt = time.Now().UTC()
		// A successful pull means we have observed everything
		// up to the server's head as of the request, so the
		// rebase-needed flag clears (sync.DRAFT.2). If the user
		// pulled without a prior conflict, this is a no-op.
		wm.NeedsRebase = false
		wm.LastConflictHead = ""
		if err := SaveWatermark(args.WorkspaceRoot, wm); err != nil {
			return pulled, err
		}
	}
	return pulled, nil
}

// SyncResult is the combined outcome of Sync.
type SyncResult struct {
	Pulled int
	Push   PushResult
}

// Sync runs PushOnly then PullOnly against the configured remote
// (sync.CLIENT.3). The push-first ordering keeps watermark
// advancement clean: a fresh local syncing into a non-empty server
// skips the push and pulls everything; a non-empty local syncing
// into a fresh server pushes everything; both-non-empty returns
// *ConflictError, surfacing the divergence for the rebase engine
// (sync.GIT.*) to handle.
func (c *Client) Sync(ctx context.Context, workspaceRoot, remote string, eventsLogPath string) (SyncResult, error) {
	args := RunArgs{
		WorkspaceRoot: workspaceRoot,
		Remote:        remote,
		EventsLogPath: eventsLogPath,
	}
	push, err := c.PushOnly(ctx, args)
	if err != nil {
		return SyncResult{Push: push}, err
	}
	pulled, err := c.PullOnly(ctx, args)
	return SyncResult{Push: push, Pulled: pulled}, err
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

// Bootstrap redeems the central's one-time admin claim token
// (central-node.BOOT.2). The flow:
//
//  1. Client makes sure it has a Bearer token (via its
//     configured Signer running through the standard
//     challenge/verify handshake — same as Push/Pull).
//  2. POST /admin/bootstrap with the token in the body.
//  3. On success the response is a proto.BootstrapResponse
//     with the org id + name + the redeemer's fingerprint
//     (echoed back for the client's audit log).
//
// Returns a typed error when the token is rejected so the CLI
// can surface "token is invalid or already redeemed" without
// guessing from the status code.
func (c *Client) Bootstrap(ctx context.Context, token string) (proto.BootstrapResponse, error) {
	if token == "" {
		return proto.BootstrapResponse{}, fmt.Errorf("sync: bootstrap token is required")
	}
	body, err := json.Marshal(proto.BootstrapRequest{Token: token})
	if err != nil {
		return proto.BootstrapResponse{}, err
	}
	resp, err := c.doAuthorized(ctx, http.MethodPost, c.baseURL+"/admin/bootstrap", func() (io.Reader, error) {
		return bytes.NewReader(body), nil
	})
	if err != nil {
		return proto.BootstrapResponse{}, fmt.Errorf("sync: POST /admin/bootstrap: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proto.BootstrapResponse{}, decodeError(resp)
	}
	var out proto.BootstrapResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return proto.BootstrapResponse{}, fmt.Errorf("sync: decode /admin/bootstrap response: %w", err)
	}
	return out, nil
}
