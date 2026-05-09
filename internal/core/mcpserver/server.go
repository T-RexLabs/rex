// Package mcpserver implements Rex's built-in MCP server
// (tools.INTROSPECT.*). The server speaks JSON-RPC NDJSON on
// stdio per the Model Context Protocol; it is spawned as a
// subprocess via `rex mcp` and auto-attached to every harness
// session Rex starts.
//
// What it exposes (v1):
//
//	rex.workspace.brief()         — fetch the harness brief
//	rex.spec.list()               — enumerate workspace specs
//	rex.spec.read(id)             — fetch a spec's YAML body
//	rex.spec.validate(id?)        — run the schema validator
//	rex.spec.verify(id?)          — exercise structured proof
//	rex.spec.runs(id, task?)      — list runs that cite a spec
//	rex.events.recent(type?, n?)  — tail audit-class events
//
// All v1 tools are read-only. Mutating tools (INTROSPECT.4)
// land separately once the permission-flow integration is wired.
//
// The implementation deliberately rolls its own minimal MCP
// handler rather than depending on an external SDK: the protocol
// surface we need (initialize, tools/list, tools/call, ping,
// shutdown) is small, the wire format is plain JSON-RPC over
// NDJSON, and we already own a comparable runtime in
// internal/core/acp/. Keeping it in-tree avoids a new external
// dependency for a 200-line server (overview.ENG.1 compatible).
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ProtocolVersion is the MCP version the server advertises in
// its initialize response. Conforms to the modelcontextprotocol
// 2024-11-05 spec, the version most current harnesses target.
const ProtocolVersion = "2024-11-05"

// ServerInfo describes the server in initialize responses.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities is the slice of MCP capability flags Rex's
// server advertises. Only `tools` for v1 — Rex doesn't host
// resources / prompts / logging streams (those land later if
// authors want them).
type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability is the per-method capability descriptor for
// the tools surface. listChanged is false because Rex's tool
// set is fixed for the lifetime of one MCP server process.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// Tool is one entry in the tools/list response. Description and
// InputSchema are surfaced verbatim to the model; both should
// be precise enough that the harness picks the right tool
// without trial calls.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolHandler is the dispatch shape for one tool. args is the
// raw JSON arguments object the caller supplied; the handler
// returns a Result whose Content is rendered to the model.
type ToolHandler func(ctx context.Context, args json.RawMessage) (Result, error)

// Result is the tools/call response. Content is a list of
// {type: "text", text: "..."} blocks per the MCP spec; the
// helper TextResult wraps a string into the canonical shape.
// IsError flags the call as failed so the harness shows an
// error UI instead of treating the content as a normal
// response.
type Result struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is one text fragment in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextResult is the canonical "single text block, success"
// constructor — the shape 80% of tools want.
func TextResult(text string) Result {
	return Result{Content: []ContentBlock{{Type: "text", Text: text}}}
}

// ErrorResult is the failure-side counterpart. The harness
// renders this as an error rather than a normal response.
func ErrorResult(text string) Result {
	return Result{Content: []ContentBlock{{Type: "text", Text: text}}, IsError: true}
}

// Server is the MCP JSON-RPC server. Construct via New, register
// tools via Register, then call Serve(reader, writer) to drive
// the loop. Tests pump the loop via in-memory pipes; production
// uses os.Stdin / os.Stdout.
type Server struct {
	info ServerInfo

	mu       sync.Mutex
	tools    map[string]Tool
	handlers map[string]ToolHandler
	order    []string
}

// New constructs a Server with no tools registered. Call
// Register for each tool before invoking Serve.
func New(info ServerInfo) *Server {
	return &Server{
		info:     info,
		tools:    make(map[string]Tool),
		handlers: make(map[string]ToolHandler),
	}
}

// Register adds tool to the server. Calling Register with a
// previously-registered name overwrites the prior entry —
// matches the same "single registration per name" expectation
// the runner's PrimitiveRegistry has, but tests sometimes
// re-register so we stay permissive rather than panic.
func (s *Server) Register(tool Tool, handler ToolHandler) {
	if tool.Name == "" {
		panic("mcpserver: empty tool name")
	}
	if handler == nil {
		panic("mcpserver: nil handler for " + tool.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[tool.Name]; !exists {
		s.order = append(s.order, tool.Name)
	}
	s.tools[tool.Name] = tool
	s.handlers[tool.Name] = handler
}

// Serve runs the JSON-RPC loop on the supplied reader/writer
// until either side closes or ctx is cancelled. Returns nil on
// orderly shutdown; non-nil on a transport error.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 4MB max line — generous for big spec content
	enc := json.NewEncoder(out)
	encMu := sync.Mutex{}
	writeReply := func(reply rpcMessage) error {
		encMu.Lock()
		defer encMu.Unlock()
		return enc.Encode(reply)
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Malformed line — emit a parse-error reply per
			// JSON-RPC and keep going. The peer might recover.
			_ = writeReply(rpcParseError(nil, err))
			continue
		}
		if msg.Method == "" {
			// Notification responses or status frames; ignore.
			continue
		}
		// Dispatch synchronously; tools/call handlers may take
		// time but they're already wrapped in their own ctx.
		// Notifications (no id) get no reply.
		reply := s.dispatch(ctx, msg)
		if msg.ID != nil && reply != nil {
			if err := writeReply(*reply); err != nil {
				return fmt.Errorf("mcpserver: write reply: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcpserver: read: %w", err)
	}
	return nil
}

// dispatch routes one request to its handler and builds the
// reply. Returns nil for notifications (no response expected).
func (s *Server) dispatch(ctx context.Context, msg rpcMessage) *rpcMessage {
	switch msg.Method {
	case "initialize":
		return s.replyInitialize(msg)
	case "ping":
		return rpcReply(msg.ID, json.RawMessage(`{}`))
	case "tools/list":
		return s.replyToolsList(msg)
	case "tools/call":
		return s.replyToolsCall(ctx, msg)
	case "shutdown":
		return rpcReply(msg.ID, json.RawMessage(`{}`))
	default:
		return rpcMethodNotFound(msg.ID, msg.Method)
	}
}

func (s *Server) replyInitialize(msg rpcMessage) *rpcMessage {
	body, _ := json.Marshal(map[string]any{
		"protocolVersion": ProtocolVersion,
		"serverInfo":      s.info,
		"capabilities": Capabilities{
			Tools: &ToolsCapability{ListChanged: false},
		},
	})
	return rpcReply(msg.ID, body)
}

func (s *Server) replyToolsList(msg rpcMessage) *rpcMessage {
	s.mu.Lock()
	out := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.tools[name])
	}
	s.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"tools": out})
	return rpcReply(msg.ID, body)
}

func (s *Server) replyToolsCall(ctx context.Context, msg rpcMessage) *rpcMessage {
	var params toolsCallParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return rpcInvalidParams(msg.ID, err)
	}
	if params.Name == "" {
		return rpcInvalidParams(msg.ID, errors.New("tool name is required"))
	}
	s.mu.Lock()
	handler := s.handlers[params.Name]
	s.mu.Unlock()
	if handler == nil {
		return rpcMethodNotFound(msg.ID, "tools/call:"+params.Name)
	}
	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	res, err := handler(ctx, args)
	if err != nil {
		body, _ := json.Marshal(ErrorResult(err.Error()))
		return rpcReply(msg.ID, body)
	}
	body, _ := json.Marshal(res)
	return rpcReply(msg.ID, body)
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// rpcMessage is one wire frame on the JSON-RPC stdio surface.
// Fields are loose so the same struct works for requests,
// responses, and notifications.
type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

// rpcError is the failure shape per JSON-RPC 2.0.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcReply(id *json.RawMessage, result json.RawMessage) *rpcMessage {
	return &rpcMessage{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcParseError(id *json.RawMessage, err error) rpcMessage {
	return rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}}
}

func rpcInvalidParams(id *json.RawMessage, err error) *rpcMessage {
	return &rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}}
}

func rpcMethodNotFound(id *json.RawMessage, method string) *rpcMessage {
	return &rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32601, Message: "method not found: " + method}}
}
