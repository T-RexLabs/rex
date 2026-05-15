package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/asabla/rex/internal/core/runner"
	internalweb "github.com/asabla/rex/internal/web"
)

// categorizeFrame inspects an ACP frame and returns the typed view
// the run-detail template renders by default. Returns nil when the
// frame doesn't match any known ACP shape — the caller should fall
// back to the raw JSON view in that case.
//
// Kept deliberately schema-loose: we look at frame.method and
// frame.params.update.type and pull text/tool data out of whatever
// shape the upstream ACP version uses. Unknown update types still
// produce a "meta" view with the method name so something legible
// always renders.
func categorizeFrame(ev runner.HarnessFrameEvent, hl *internalweb.Highlighter) *frameView {
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

	if raw.Method == "session/update" && len(raw.Params) > 0 {
		view := decodeUpdate(raw.Params, hl)
		if view != nil {
			return view
		}
	}

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

// updatePayload is the union of fields we look at across every
// session/update variant. Most fields are optional — different
// harnesses populate different subsets. Two discriminator fields
// exist in the wild: `sessionUpdate` (Anthropic claude-agent-acp)
// and `type` (broader ACP); we accept both.
type updatePayload struct {
	SessionUpdate string `json:"sessionUpdate"`
	Type          string `json:"type"`

	// Agent text variants. The Claude bridge nests text under
	// `content` (single object); other ACP variants put it directly
	// in `text` or in `content[]`.
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`

	// Tool calls — many shapes in the wild:
	// - Anthropic claude-agent-acp uses top-level `title`, `kind`,
	//   `rawInput`, `status`, `toolCallId`, plus `_meta.claudeCode.toolName`.
	// - Older ACP variants nested under `tool`/`toolCall` with
	//   inner `name` + `arguments`.
	Tool       json.RawMessage `json:"tool"`
	ToolCall   json.RawMessage `json:"toolCall"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
	Title      string          `json:"title"`
	ToolKind   string          `json:"kind"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	ToolCallID string          `json:"toolCallId"`
	Status     string          `json:"status"`

	Meta struct {
		ClaudeCode struct {
			ToolName     string          `json:"toolName"`
			ToolResponse json.RawMessage `json:"toolResponse"`
		} `json:"claudeCode"`
	} `json:"_meta"`

	Used              int64           `json:"used"`
	Size              int64           `json:"size"`
	AvailableCommands json.RawMessage `json:"availableCommands"`
}

// decodeUpdate parses a session/update params payload into a frameView.
// All field heuristics live here so the rest of the pipeline can stay
// untyped about which ACP dialect produced the frame.
func decodeUpdate(params json.RawMessage, hl *internalweb.Highlighter) *frameView {
	var p struct {
		Update updatePayload `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	u := p.Update

	kind := u.SessionUpdate
	if kind == "" {
		kind = u.Type
	}
	switch kind {
	case "":
		return nil
	case "agent_message_chunk", "agent_message", "user_message_chunk":
		role := "assistant"
		if kind == "user_message_chunk" {
			role = "user"
		}
		text := extractText(u.Text, u.Content)
		if text == "" {
			return nil
		}
		return &frameView{Kind: "agent_text", Role: role, Text: text}
	case "agent_thought_chunk", "agent_thought":
		text := extractText(u.Text, u.Content)
		if text == "" {
			return nil
		}
		return &frameView{Kind: "agent_thought", Role: "assistant", Text: text}
	case "tool_call", "tool_call_start":
		return buildToolFrame("tool_call", u, hl)
	case "tool_call_complete", "tool_call_update", "tool_result":
		return buildToolFrame("tool_result", u, hl)
	case "plan_change", "plan":
		return &frameView{Kind: "plan", Role: "assistant", Text: extractText(u.Text, u.Content)}
	case "usage_update":
		return &frameView{
			Kind:   "meta",
			Role:   "system",
			Method: "usage",
			Text:   formatUsage(u.Used, u.Size),
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

// buildToolFrame constructs a tool_call / tool_result frameView from
// the loose updatePayload. The result frame normalises status into
// {pending, running, ok, error} so the template doesn't have to
// branch on every per-harness wording.
func buildToolFrame(kind string, u updatePayload, hl *internalweb.Highlighter) *frameView {
	name, args := extractTool(u)
	subtitle := extractToolSubtitle(name, u)
	output := extractContentText(u.Content)

	status := normalizeToolStatus(kind, u.Status)
	return &frameView{
		Kind:       kind,
		Role:       "tool",
		ToolName:   name,
		ToolCallID: u.ToolCallID,
		Subtitle:   subtitle,
		ToolArgs:   jsonHTML(args, hl),
		ToolOutput: output,
		Status:     status,
	}
}

// normalizeToolStatus collapses the per-harness vocabulary
// (pending/in_progress/completed/failed/ok/…) into the four
// values the template + JS know how to colour.
func normalizeToolStatus(kind, raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ok", "success", "completed":
		return "ok"
	case "error", "failed", "failure":
		return "error"
	case "pending":
		return "pending"
	case "running", "in_progress", "inprogress":
		return "running"
	case "":
		if kind == "tool_call" {
			return "pending"
		}
		return "ok"
	default:
		return raw
	}
}

// extractToolSubtitle pulls a useful one-liner from rawInput
// when the tool itself is a generic dispatcher (Skill, Bash, …).
// Returns "" when nothing useful is available.
func extractToolSubtitle(name string, u updatePayload) string {
	if len(u.RawInput) == 0 {
		return ""
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(u.RawInput, &probe); err != nil {
		return ""
	}
	switch strings.ToLower(name) {
	case "skill":
		return firstString(probe, "skill")
	case "bash":
		return firstString(probe, "command", "description")
	case "read":
		return firstString(probe, "file_path", "path")
	case "edit", "write":
		return firstString(probe, "file_path", "path")
	case "grep":
		return firstString(probe, "pattern")
	case "glob":
		return firstString(probe, "pattern")
	}
	// Fallback: if rawInput has exactly one short string field, use
	// it as a subtitle. Keeps unknown tools legible without dumping
	// all their args inline.
	if len(probe) == 1 {
		for _, v := range probe {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && len(s) <= 80 {
				return s
			}
		}
	}
	return ""
}

func firstString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// extractContentText flattens the ACP content[] array into a plain
// string. Each entry can be a top-level {type:"text", text:"..."}
// or nested {type:"content", content:{type:"text", text:"..."}}
// (Anthropic's flavour). Non-text entries are skipped.
func extractContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	// Try array-of-blocks first.
	var arr []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(content, &arr); err == nil {
		var b strings.Builder
		for _, blk := range arr {
			if blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(blk.Text)
				continue
			}
			if len(blk.Content) > 0 {
				var inner struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(blk.Content, &inner); err == nil && inner.Text != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(inner.Text)
				}
			}
		}
		return b.String()
	}
	// Fall back to single object.
	var single struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &single); err == nil {
		return single.Text
	}
	return ""
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

// extractTool resolves the tool's display name + arguments JSON
// from whichever fields the harness happened to fill. Order:
//
//  1. `toolName` (older ACP dialects)
//  2. nested `tool.name` / `toolCall.name`
//  3. `_meta.claudeCode.toolName`
//  4. `title` (Anthropic claude-agent-acp)
//  5. literal "(unnamed)"
//
// Arguments fall back through `arguments` → nested `arguments` →
// `rawInput` so the args drawer is non-empty for Claude tool calls.
func extractTool(u updatePayload) (string, json.RawMessage) {
	name := u.ToolName
	args := u.Arguments

	// Nested tool/toolCall objects (legacy).
	for _, body := range []json.RawMessage{u.Tool, u.ToolCall} {
		if len(body) == 0 {
			continue
		}
		var probe struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(body, &probe); err == nil {
			if name == "" {
				name = probe.Name
			}
			if len(args) == 0 {
				args = probe.Arguments
			}
		}
	}

	if name == "" {
		name = u.Meta.ClaudeCode.ToolName
	}
	if name == "" {
		name = u.Title
	}
	if name == "" {
		name = "(unnamed)"
	}
	if len(args) == 0 && len(u.RawInput) > 0 {
		args = u.RawInput
	}
	return name, args
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
// markup tweak lands in one place. data-frame-* and
// data-tool-call-id attributes match the server-rendered versions
// so the JS coalescer treats them identically.
func renderFrameCardHTML(row runEventRow, fv *frameView) string {
	var body string
	kindLabel := fv.Kind
	switch fv.Kind {
	case "agent_text":
		kindLabel = "message"
	case "agent_thought":
		kindLabel = "thought"
	}
	roleLabel := fv.Role
	if roleLabel == "user" {
		roleLabel = "you"
	}
	switch fv.Kind {
	case "agent_text":
		body = `<div class="frame-body frame-text" data-frame-text>` + html.EscapeString(fv.Text) + `</div>`
	case "agent_thought":
		body = `<div class="frame-body frame-text frame-thought" data-frame-text>` + html.EscapeString(fv.Text) + `</div>`
	case "tool_call":
		body = renderToolBodyHTML("→", fv)
	case "tool_result":
		body = renderToolBodyHTML("←", fv)
	default:
		body = `<div class="frame-body muted small">` + html.EscapeString(fv.Text) + `</div>`
	}

	header := `<header class="event-head">` +
		`<span class="frame-role frame-role-` + html.EscapeString(fv.Role) + `">` + html.EscapeString(roleLabel) + `</span>` +
		`<span class="frame-kind muted small">` + html.EscapeString(kindLabel)
	if fv.Method != "" {
		header += ` · <code>` + html.EscapeString(fv.Method) + `</code>`
	}
	header += `</span><time class="event-time">` + html.EscapeString(row.Timestamp) + `</time></header>`

	debug := `<div class="frame-debug"><details><summary class="muted small">raw frame · <code>` +
		html.EscapeString(row.ID) + `</code></summary>` +
		`<pre class="event-body chroma"><code class="language-json">` + string(row.Payload) + `</code></pre>` +
		`</details></div>`

	toolID := ""
	if fv.ToolCallID != "" {
		toolID = ` data-tool-call-id="` + html.EscapeString(fv.ToolCallID) + `"`
	}

	return `<article class="event frame frame-` + html.EscapeString(fv.Kind) +
		`" data-event-id="` + html.EscapeString(row.ID) +
		`" data-frame-kind="` + html.EscapeString(fv.Kind) +
		`" data-frame-role="` + html.EscapeString(fv.Role) + `"` + toolID + `>` +
		header + body + debug + `</article>`
}

// renderToolBodyHTML draws the inner card body for a tool_call /
// tool_result. The outer markup is shared by both branches so the
// JS coalescer can rewrite any tool card in place when a later
// tool_call_update arrives with the same toolCallId.
func renderToolBodyHTML(arrow string, fv *frameView) string {
	body := `<div class="frame-body frame-tool" data-tool-body>` +
		`<span class="tool-arrow">` + html.EscapeString(arrow) + `</span>` +
		`<code class="tool-name" data-tool-name>` + html.EscapeString(fv.ToolName) + `</code>`
	if fv.Subtitle != "" {
		body += `<span class="tool-subtitle muted small" data-tool-subtitle>` +
			html.EscapeString(fv.Subtitle) + `</span>`
	} else {
		body += `<span class="tool-subtitle muted small" data-tool-subtitle hidden></span>`
	}
	body += renderToolStatusPill(fv.Status)
	if fv.ToolArgs != "" {
		body += `<details class="tool-args" data-tool-args><summary class="muted small">args</summary>` +
			`<pre class="chroma"><code class="language-json">` + string(fv.ToolArgs) + `</code></pre></details>`
	}
	if fv.ToolOutput != "" {
		body += `<div class="tool-output" data-tool-output>` + html.EscapeString(truncateOutput(fv.ToolOutput)) + `</div>`
	} else {
		body += `<div class="tool-output" data-tool-output hidden></div>`
	}
	body += `</div>`
	return body
}

func renderToolStatusPill(status string) string {
	if status == "" {
		status = "pending"
	}
	cls := status
	switch status {
	case "ok", "running", "pending", "error":
		// keep
	default:
		cls = "ok"
	}
	return `<span class="pill pill-` + html.EscapeString(cls) + `" data-tool-status>` +
		html.EscapeString(status) + `</span>`
}

func truncateOutput(s string) string {
	const max = 400
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// lifecycleSummary returns ("run started · ...", true) for the
// runner lifecycle events the run-detail page renders compactly,
// (\"\", false) for everything else. Mirrors the loadRunDetail
// lookup so initial render and SSE-streamed events agree.
func lifecycleSummary(decoded any) (string, bool) {
	switch ev := decoded.(type) {
	case runner.RunStartedEvent:
		return "run started", true
	case runner.RunCompletedEvent:
		return "run completed", true
	case runner.RunCancelledEvent:
		return "run cancelled", true
	case runner.NodeStartedEvent:
		return "node started · " + string(ev.NodeID), true
	case runner.NodeSucceededEvent:
		return "node succeeded · " + string(ev.NodeID), true
	case runner.NodeRetriedEvent:
		return "node retried · " + string(ev.NodeID), true
	}
	return "", false
}

// renderCompactLifecycleHTML produces the same single-line dim
// row the initial server render uses for run.*/node.* events;
// kept here so the SSE handler can emit identical markup. The
// run id is plumbed through as a data attribute so DOM-side
// filtering (and tests) can locate the row.
func renderCompactLifecycleHTML(row runEventRow, eventType, summary, runID string) string {
	return `<div class="event-compact event-compact-lifecycle" data-event-id="` +
		html.EscapeString(row.ID) + `" data-run-id="` +
		html.EscapeString(runID) + `">` +
		`<time class="event-time">` + html.EscapeString(row.Timestamp) + `</time>` +
		`<span class="compact-bullet">●</span>` +
		`<span class="compact-label"><code>` + html.EscapeString(eventType) +
		`</code> · ` + html.EscapeString(summary) + `</span></div>`
}

// renderCompactMetaHTML is the SSE-side compact row for meta-class
// harness frames (usage updates, session boilerplate).
func renderCompactMetaHTML(row runEventRow, fv *frameView) string {
	label := fv.Method
	if fv.Text != "" {
		if label != "" {
			label += " · "
		}
		label += fv.Text
	}
	return `<div class="event-compact event-compact-meta" data-event-id="` +
		html.EscapeString(row.ID) + `">` +
		`<time class="event-time">` + html.EscapeString(row.Timestamp) + `</time>` +
		`<span class="compact-bullet">·</span>` +
		`<span class="compact-label">` + html.EscapeString(label) + `</span></div>`
}

// jsonHTML renders a json.RawMessage either as chroma-highlighted
// HTML (when hl is non-nil) or as plain escaped text. Empty input
// returns an empty template.HTML so the template can {{if}} on it.
func jsonHTML(body json.RawMessage, hl *internalweb.Highlighter) template.HTML {
	if len(body) == 0 {
		return ""
	}
	if hl != nil {
		return hl.HighlightJSON(body)
	}
	return template.HTML(html.EscapeString(internalweb.PrettyJSON(body)))
}
