package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"

	"github.com/asabla/rex/internal/core/runner"
)

// categorizeFrame inspects an ACP frame and returns the typed view
// the run-detail template renders by default. Returns nil when the
// frame doesn't match any known ACP shape — the caller should fall
// back to the raw JSON view in that case.
//
// Kept deliberately schema-loose: we look at frame.method and
// frame.params.update.type and pull text out of whatever shape the
// upstream ACP version uses. Unknown update types still produce a
// "meta" view with the method name so something legible always
// renders.
func categorizeFrame(ev runner.HarnessFrameEvent, hl *Highlighter) *frameView {
	if len(ev.Frame) == 0 {
		return nil
	}
	var raw struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(ev.Frame, &raw); err != nil {
		return nil
	}

	// Notifications carry the actual content. session/update is
	// the dominant one; session/request_permission lands as its
	// own runner event, so we don't double-render here.
	if raw.Method == "session/update" && len(raw.Params) > 0 {
		view := decodeUpdate(raw.Params, hl)
		if view != nil {
			return view
		}
	}

	// Responses to client-issued calls (session/new returning a
	// session id, session/prompt returning a stop_reason). The
	// inner ACP frame has no method on responses, so we use
	// whatever ev.Method the runtask layer captured plus the
	// payload shape to figure out what kind of response this is.
	if len(raw.Result) > 0 || len(raw.Error) > 0 {
		method := ev.Method
		if method == "" {
			method = "response"
		}
		text := "ok"
		if len(raw.Error) > 0 {
			text = "error"
		} else if reason := extractStopReason(raw.Result); reason != "" {
			text = "stop_reason=" + reason
		} else if sid := extractResultSessionID(raw.Result); sid != "" {
			text = "session=" + sid
		}
		return &frameView{Kind: "meta", Role: "system", Method: method, Text: text}
	}
	if raw.Method != "" {
		// A notification we don't have a typed renderer for.
		return &frameView{Kind: "meta", Role: "system", Method: raw.Method}
	}
	return nil
}

// extractResultSessionID pulls sessionId out of the result body
// (used for the session/new response which establishes the id).
func extractResultSessionID(result json.RawMessage) string {
	var probe struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &probe); err == nil {
		return probe.SessionID
	}
	return ""
}

// decodeUpdate parses a session/update params payload. The Anthropic
// ACP bridge uses `sessionUpdate` as the discriminator (the broader
// ACP spec uses `type` in some versions); we accept both so a future
// bridge bump in either direction stays forward-compatible.
func decodeUpdate(params json.RawMessage, hl *Highlighter) *frameView {
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Type          string `json:"type"`

			// Agent text variants. The Claude bridge nests text
			// under `content` (single object); other ACP variants
			// put it directly in `text` or in `content[]`.
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`

			// Tool calls.
			Tool      json.RawMessage `json:"tool"`
			ToolCall  json.RawMessage `json:"toolCall"`
			ToolName  string          `json:"toolName"`
			Arguments json.RawMessage `json:"arguments"`
			Status    string          `json:"status"`

			// Claude-specific meters / hints we surface as a
			// muted info row rather than agent text.
			Used             int64           `json:"used"`
			Size             int64           `json:"size"`
			AvailableCommands json.RawMessage `json:"availableCommands"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	kind := p.Update.SessionUpdate
	if kind == "" {
		kind = p.Update.Type
	}
	switch kind {
	case "":
		return nil
	case "agent_message_chunk", "agent_message", "user_message_chunk":
		role := "assistant"
		if kind == "user_message_chunk" {
			role = "user"
		}
		text := extractText(p.Update.Text, p.Update.Content)
		if text == "" {
			return nil
		}
		return &frameView{Kind: "agent_text", Role: role, Text: text}
	case "agent_thought_chunk", "agent_thought":
		text := extractText(p.Update.Text, p.Update.Content)
		if text == "" {
			return nil
		}
		return &frameView{Kind: "agent_thought", Role: "assistant", Text: text}
	case "tool_call", "tool_call_start":
		name, args := extractTool(p.Update.Tool, p.Update.ToolCall, p.Update.ToolName, p.Update.Arguments)
		return &frameView{
			Kind:     "tool_call",
			Role:     "tool",
			ToolName: name,
			ToolArgs: jsonHTML(args, hl),
		}
	case "tool_call_complete", "tool_call_update", "tool_result":
		name, args := extractTool(p.Update.Tool, p.Update.ToolCall, p.Update.ToolName, p.Update.Arguments)
		status := p.Update.Status
		if status == "" {
			status = "ok"
		}
		return &frameView{
			Kind:     "tool_result",
			Role:     "tool",
			ToolName: name,
			ToolArgs: jsonHTML(args, hl),
			Status:   status,
		}
	case "plan_change", "plan":
		return &frameView{Kind: "plan", Role: "assistant", Text: extractText(p.Update.Text, p.Update.Content)}
	case "usage_update":
		return &frameView{
			Kind:   "meta",
			Role:   "system",
			Method: "usage",
			Text:   formatUsage(p.Update.Used, p.Update.Size),
		}
	case "available_commands_update":
		return &frameView{
			Kind:   "meta",
			Role:   "system",
			Method: "commands",
			Text:   "harness commands registered",
		}
	default:
		return &frameView{Kind: "meta", Role: "system", Method: "session/update", Text: kind}
	}
}

// formatUsage renders a token meter compactly: "30,265 / 200,000".
func formatUsage(used, size int64) string {
	if size <= 0 {
		if used <= 0 {
			return "usage"
		}
		return fmt.Sprintf("%d tokens", used)
	}
	return fmt.Sprintf("%d / %d tokens", used, size)
}

func extractText(plain string, content json.RawMessage) string {
	if plain != "" {
		return plain
	}
	if len(content) == 0 {
		return ""
	}
	var single struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &single); err == nil && single.Text != "" {
		return single.Text
	}
	var arr []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &arr); err == nil {
		var out string
		for _, b := range arr {
			out += b.Text
		}
		return out
	}
	return ""
}

func extractTool(tool, toolCall json.RawMessage, name string, args json.RawMessage) (string, json.RawMessage) {
	if name != "" {
		return name, args
	}
	for _, body := range []json.RawMessage{tool, toolCall} {
		if len(body) == 0 {
			continue
		}
		var probe struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(body, &probe); err == nil && probe.Name != "" {
			outArgs := args
			if len(outArgs) == 0 {
				outArgs = probe.Arguments
			}
			return probe.Name, outArgs
		}
	}
	return "(unnamed)", args
}

func extractStopReason(result json.RawMessage) string {
	var probe struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(result, &probe); err == nil {
		return probe.StopReason
	}
	return ""
}

// renderFrameCardHTML produces the same HTML the run_detail.tmpl
// frame branch produces, used by the SSE handler to emit live
// frames. Kept here (rather than duplicated in two files) so any
// markup tweak lands in one place. data-frame-* attributes match
// the server-rendered versions so the JS coalescer treats them
// identically.
func renderFrameCardHTML(row runEventRow, fv *frameView) string {
	var body string
	switch fv.Kind {
	case "agent_text":
		body = `<div class="frame-body frame-text" data-frame-text>` + html.EscapeString(fv.Text) + `</div>`
	case "agent_thought":
		body = `<div class="frame-body frame-text frame-thought">` + html.EscapeString(fv.Text) + `</div>`
	case "tool_call":
		body = `<div class="frame-body frame-tool"><span class="tool-arrow">→</span>` +
			`<code class="tool-name">` + html.EscapeString(fv.ToolName) + `</code>`
		if fv.ToolArgs != "" {
			body += `<details class="tool-args"><summary class="muted small">args</summary>` +
				`<pre class="chroma"><code class="language-json">` + string(fv.ToolArgs) + `</code></pre></details>`
		}
		body += `</div>`
	case "tool_result":
		statusClass := "ok"
		if fv.Status != "" && fv.Status != "ok" {
			statusClass = "error"
		}
		body = `<div class="frame-body frame-tool"><span class="tool-arrow">←</span>` +
			`<code class="tool-name">` + html.EscapeString(fv.ToolName) + `</code>` +
			`<span class="pill pill-` + statusClass + `">` + html.EscapeString(fv.Status) + `</span>`
		if fv.ToolArgs != "" {
			body += `<details class="tool-args"><summary class="muted small">result</summary>` +
				`<pre class="chroma"><code class="language-json">` + string(fv.ToolArgs) + `</code></pre></details>`
		}
		body += `</div>`
	default:
		body = `<div class="frame-body muted small">` + html.EscapeString(fv.Text) + `</div>`
	}

	header := `<header class="event-head">` +
		`<span class="frame-role frame-role-` + html.EscapeString(fv.Role) + `">` + html.EscapeString(fv.Role) + `</span>` +
		`<span class="frame-kind muted small">` + html.EscapeString(fv.Kind)
	if fv.Method != "" {
		header += ` · <code>` + html.EscapeString(fv.Method) + `</code>`
	}
	header += `</span><time class="event-time">` + html.EscapeString(row.Timestamp) + `</time></header>`

	debug := `<div class="frame-debug"><details><summary class="muted small">raw frame · <code>` +
		html.EscapeString(row.ID) + `</code></summary>` +
		`<pre class="event-body chroma"><code class="language-json">` + string(row.Payload) + `</code></pre>` +
		`</details></div>`

	return `<article class="event frame frame-` + html.EscapeString(fv.Kind) +
		`" data-event-id="` + html.EscapeString(row.ID) +
		`" data-frame-kind="` + html.EscapeString(fv.Kind) +
		`" data-frame-role="` + html.EscapeString(fv.Role) + `">` +
		header + body + debug + `</article>`
}

// jsonHTML renders a json.RawMessage either as chroma-highlighted
// HTML (when hl is non-nil) or as plain escaped text. Empty input
// returns an empty template.HTML so the template can {{if}} on it.
func jsonHTML(body json.RawMessage, hl *Highlighter) template.HTML {
	if len(body) == 0 {
		return ""
	}
	if hl != nil {
		return hl.HighlightJSON(body)
	}
	return template.HTML(html.EscapeString(PrettyJSON(body)))
}
