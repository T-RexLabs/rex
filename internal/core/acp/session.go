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
	MethodSessionPrompt            = "session/prompt"
	MethodSessionCancel            = "session/cancel"
	MethodSessionRequestPermission = "session/request_permission"
)

// MCPServer describes one MCP server attachment passed to session/new
// per execution.ACP.5. Rex passes these straight through to the
// harness; no portal proxy intervenes in v1 (overview.SCOPE).
//
// Shape mirrors the ACP stdio variant — the only one v1 needs and the
// baseline every ACP agent must support. Command is the executable
// path (string), Args is the argv tail (array), Env is an array of
// {name,value} pairs (not a map). All three fields are required on
// the wire and serialize as `[]` rather than `null` when empty,
// because the upstream Zod validator rejects null/undefined.
type MCPServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []EnvVariable `json:"env"`
}

// EnvVariable is one entry of MCPServer.Env. ACP requires env to be
// an array of {name,value} objects rather than a map.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SessionNewParams is the payload for session/new. Mirrors the
// upstream Agent Client Protocol shape: cwd is required (the harness
// uses it as the working directory for tool calls); mcpServers is
// required as an array (empty arrays must serialize as `[]`, not
// `null` or `omitted`, so the field has no omitempty and the
// constructor in NewSession ensures a non-nil slice goes on the
// wire).
type SessionNewParams struct {
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers"`
}

// SessionNewResult is the response from session/new.
type SessionNewResult struct {
	SessionID string          `json:"sessionId"`
	Extra     json.RawMessage `json:"-"`
}

// PromptContentBlock is one block of a session/prompt content array.
// V1 only ships text blocks; image and resource blocks land if and
// when a real use case demands them.
type PromptContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextPromptBlocks builds the most common shape — a single text
// block — so callers don't have to know the wire format.
func TextPromptBlocks(text string) []PromptContentBlock {
	return []PromptContentBlock{{Type: "text", Text: text}}
}

// SessionPromptParams is the payload for session/prompt: the user's
// message, addressed to a session opened by session/new.
type SessionPromptParams struct {
	SessionID string               `json:"sessionId"`
	Prompt    []PromptContentBlock `json:"prompt"`
}

// SessionPromptResult is the response to session/prompt. The bridge
// returns metadata when the model finishes (e.g. stop_reason); we
// keep the raw payload so adapter-specific extras stay accessible
// without growing the struct.
type SessionPromptResult struct {
	StopReason string          `json:"stopReason,omitempty"`
	Extra      json.RawMessage `json:"-"`
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
// Ensures MCPServers serializes as `[]` rather than `null` when the
// caller passes a nil slice — the upstream bridge rejects null with
// a Zod-style "expected array" error.
func (c *Client) NewSession(ctx context.Context, params SessionNewParams) (SessionNewResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServer{}
	}
	for i := range params.MCPServers {
		if params.MCPServers[i].Args == nil {
			params.MCPServers[i].Args = []string{}
		}
		if params.MCPServers[i].Env == nil {
			params.MCPServers[i].Env = []EnvVariable{}
		}
	}
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

// SendPrompt is a typed wrapper over Call(MethodSessionPrompt, ...).
// Per the upstream ACP, the prompt is delivered after session/new
// has returned a sessionId; the bridge streams session/update
// notifications throughout this call's lifetime and returns a
// terminal stop_reason when the model is done.
func (c *Client) SendPrompt(ctx context.Context, params SessionPromptParams) (SessionPromptResult, error) {
	raw, err := c.Call(ctx, MethodSessionPrompt, params)
	if err != nil {
		return SessionPromptResult{}, err
	}
	var res SessionPromptResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return SessionPromptResult{}, err
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
