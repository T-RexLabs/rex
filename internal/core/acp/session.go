package acp

import (
	"context"
	"encoding/json"
)

// Method names used by Rex's ACP client. The full ACP method surface
// is larger than what v1 exercises; entries land here as the
// corresponding capability is wired up.
const (
	MethodSessionNew               = "session/new"
	MethodSessionCancel            = "session/cancel"
	MethodSessionRequestPermission = "session/request_permission"
)

// MCPServer describes one MCP server attachment passed to session/new
// per execution.ACP.5. Rex passes these straight through to the
// harness; no portal proxy intervenes in v1 (overview.SCOPE).
type MCPServer struct {
	Name    string            `json:"name"`
	Command []string          `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
}

// SessionNewParams is the payload for session/new. Field names align
// with what the @agentclientprotocol harness wrappers expect; if a
// concrete harness adapter needs additional fields it can wrap this
// struct with its own.
type SessionNewParams struct {
	WorkspaceID string      `json:"workspaceId"`
	Prompt      string      `json:"prompt,omitempty"`
	Model       string      `json:"model,omitempty"`
	Mode        string      `json:"mode,omitempty"`
	MCPServers  []MCPServer `json:"mcpServers,omitempty"`
}

// SessionNewResult is the response from session/new.
type SessionNewResult struct {
	SessionID string          `json:"sessionId"`
	Extra     json.RawMessage `json:"-"`
}

// SessionCancelParams is the payload for session/cancel.
type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

// PermissionRequest is the parsed body of an inbound
// session/request_permission frame (execution.ACP.4). The engine pauses
// the run while a PermissionHandler resolves the request; that pause
// lives in the executor, not in this package.
type PermissionRequest struct {
	SessionID string          `json:"sessionId"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

// PermissionDecision is the response to a PermissionRequest.
type PermissionDecision struct {
	Granted bool   `json:"granted"`
	Note    string `json:"note,omitempty"`
}

// PermissionHandler is invoked synchronously (in a fresh goroutine per
// request) for every session/request_permission frame the harness
// sends. Returning an error sends a JSON-RPC error response instead of
// a decision; nil with Granted=false means "deny cleanly".
type PermissionHandler func(ctx context.Context, req PermissionRequest) (PermissionDecision, error)

// NewSession is a typed wrapper over Call(MethodSessionNew, ...).
func (c *Client) NewSession(ctx context.Context, params SessionNewParams) (SessionNewResult, error) {
	raw, err := c.Call(ctx, MethodSessionNew, params)
	if err != nil {
		return SessionNewResult{}, err
	}
	var res SessionNewResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return SessionNewResult{}, err
	}
	res.Extra = raw
	return res, nil
}

// CancelSession is a typed wrapper over Call(MethodSessionCancel, ...).
// Per execution.RUN.5 the executor waits up to 10s for the harness to
// honour cancel; that timer lives at the run lifecycle layer, not
// here. This call simply delivers the request.
func (c *Client) CancelSession(ctx context.Context, sessionID string) error {
	_, err := c.Call(ctx, MethodSessionCancel, SessionCancelParams{SessionID: sessionID})
	return err
}
