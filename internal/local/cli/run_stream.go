package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// streamPrinter coalesces consecutive agent_message_chunk frames
// into a single growing line so a streaming model turn doesn't
// produce N tiny rows mid-word. Same shape the web UI's JS shim
// applies; the terminal version uses no-newline writes for the
// growing turn and emits a closing newline when a non-agent_text
// event arrives.
type streamPrinter struct {
	out         io.Writer
	debug       bool
	inAgentText bool
	role        string
}

func newStreamPrinter(out io.Writer, debug bool) func(eventlog.Record) {
	sp := &streamPrinter{out: out, debug: debug}
	return sp.write
}

func (sp *streamPrinter) write(rec eventlog.Record) {
	// Debug mode: every event renders as a full block, no
	// coalescing — operators looking at debug output need to see
	// the chunk boundaries.
	if sp.debug {
		sp.closeAgentText()
		writeEventLine(sp.out, rec, true)
		return
	}

	if rec.Type == runner.EventTypeHarnessFrame {
		if text, role, ok := agentTextFromFrame(rec.Payload); ok {
			// Switch role mid-turn: close the previous role's
			// line so the prompt and the assistant's reply land
			// on separate rows even if they arrive back-to-back.
			if sp.inAgentText && sp.role != role {
				sp.closeAgentText()
			}
			if !sp.inAgentText {
				fmt.Fprintf(sp.out, "%s  %-22s  ",
					formatHLCTime(rec.Timestamp), role)
				sp.inAgentText = true
				sp.role = role
			}
			fmt.Fprint(sp.out, text)
			return
		}
	}
	sp.closeAgentText()
	writeEventLine(sp.out, rec, false)
}

func (sp *streamPrinter) closeAgentText() {
	if sp.inAgentText {
		fmt.Fprintln(sp.out)
		sp.inAgentText = false
	}
}

// agentTextFromFrame returns the text content of an agent or user
// message_chunk frame plus the speaker's role ("assistant" or
// "user"). The bool is false for any other frame shape, so the
// caller can fall through to its default renderer.
func agentTextFromFrame(payload json.RawMessage) (string, string, bool) {
	var ev runner.HarnessFrameEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return "", "", false
	}
	var frame struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(ev.Frame, &frame); err != nil || frame.Method != "session/update" {
		return "", "", false
	}
	var p struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Type          string          `json:"type"`
			Text          string          `json:"text"`
			Content       json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(frame.Params, &p); err != nil {
		return "", "", false
	}
	kind := p.Update.SessionUpdate
	if kind == "" {
		kind = p.Update.Type
	}
	role := ""
	switch kind {
	case "agent_message_chunk", "agent_message":
		role = "assistant"
	case "user_message_chunk", "user_message":
		role = "user"
	default:
		return "", "", false
	}
	return extractFrameText(kind, p.Update.Text, p.Update.Content, ""), role, true
}

// writeEventLine renders one event for a terminal. Default mode
// is the one-line "<time>  <type>  <summary>" shape; debug mode
// adds a pretty-printed payload indented under the header.
func writeEventLine(out io.Writer, rec eventlog.Record, debug bool) {
	fmt.Fprintf(out, "%s  %-22s  %s\n",
		formatHLCTime(rec.Timestamp), rec.Type, summarizeEventPayload(rec.Type, rec.Payload))
	if !debug {
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, rec.Payload, "    ", "  "); err == nil {
		fmt.Fprintf(out, "    %s\n", pretty.String())
	}
}

// summarizeEventPayload turns a runner event payload into a one-line
// summary suitable for terminal output. The cases mirror the runner
// event-type constants; unknown types fall back to a length hint.
func summarizeEventPayload(eventType string, payload json.RawMessage) string {
	switch eventType {
	case runner.EventTypeRunStarted:
		var ev runner.RunStartedEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			return fmt.Sprintf("run=%s", runner.FriendlyName(ev.RunID))
		}
	case runner.EventTypeRunCompleted:
		var ev runner.RunCompletedEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			return fmt.Sprintf("run=%s status=completed", runner.FriendlyName(ev.RunID))
		}
	case runner.EventTypeRunCancelled:
		return "status=cancelled"
	case runner.EventTypeRunAborted:
		var ev struct {
			RunID string `json:"run_id"`
			Error string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(payload, &ev); err == nil {
			if ev.Error != "" {
				return fmt.Sprintf("run=%s aborted: %s", ev.RunID, ev.Error)
			}
			return fmt.Sprintf("run=%s status=aborted", ev.RunID)
		}
	case runner.EventTypeNodeStarted, runner.EventTypeNodeSucceeded,
		runner.EventTypeNodeFailed, runner.EventTypeNodeRetried:
		var ev struct {
			NodeID string `json:"node_id"`
			Error  string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(payload, &ev); err == nil {
			if ev.Error != "" {
				return fmt.Sprintf("node=%s err=%s", ev.NodeID, ev.Error)
			}
			return fmt.Sprintf("node=%s", ev.NodeID)
		}
	case runner.EventTypePermissionRequested,
		runner.EventTypePermissionGranted,
		runner.EventTypePermissionDenied:
		var ev struct {
			NodeID    string `json:"node_id"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(payload, &ev); err == nil {
			return fmt.Sprintf("node=%s request=%s", ev.NodeID, ev.RequestID)
		}
	case runner.EventTypeHarnessFrame:
		return summarizeHarnessFrame(payload)
	}
	return fmt.Sprintf("(%d bytes)", len(payload))
}

// summarizeHarnessFrame extracts the most useful one-liner out of an
// ACP frame for the human-readable event stream. Priority:
//  1. an agent_message_chunk text — the actual model output
//  2. a tool call name — when the harness invokes a tool
//  3. the bare ACP method name as a fallback
//
// Length is capped so a long agent message doesn't blow out one
// terminal line; full content is in `rex run show <id>`. The
// discriminator field can be either `sessionUpdate` (Anthropic's
// claude-agent-acp bridge) or `type` (broader ACP spec); both
// are accepted so this stays forward-compatible.
func summarizeHarnessFrame(payload json.RawMessage) string {
	var ev runner.HarnessFrameEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Sprintf("(%d bytes)", len(payload))
	}
	var frame struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(ev.Frame, &frame)

	if len(frame.Params) > 0 {
		var p struct {
			Update struct {
				SessionUpdate string          `json:"sessionUpdate"`
				Type          string          `json:"type"`
				Content       json.RawMessage `json:"content"`
				Text          string          `json:"text,omitempty"`
				Tool          struct {
					Name string `json:"name"`
				} `json:"tool"`
				Used int64 `json:"used"`
				Size int64 `json:"size"`
			} `json:"update"`
		}
		if err := json.Unmarshal(frame.Params, &p); err == nil {
			kind := p.Update.SessionUpdate
			if kind == "" {
				kind = p.Update.Type
			}
			if kind != "" {
				text := extractFrameText(kind, p.Update.Text, p.Update.Content, p.Update.Tool.Name)
				if text != "" {
					return fmt.Sprintf("%s %s", kind, truncate(text, 80))
				}
				if kind == "usage_update" && p.Update.Size > 0 {
					return fmt.Sprintf("usage_update %d/%d tokens", p.Update.Used, p.Update.Size)
				}
				return kind
			}
		}
	}
	if ev.Method != "" {
		return ev.Method
	}
	if len(frame.Result) > 0 {
		return "result"
	}
	return "(frame)"
}

// extractFrameText pulls a human-readable string out of a
// session/update payload. Different update types put the text in
// different places; this collapses them into one return.
func extractFrameText(updateType, fallbackText string, content json.RawMessage, toolName string) string {
	if fallbackText != "" {
		return fallbackText
	}
	if toolName != "" {
		return toolName
	}
	if len(content) > 0 {
		// content may be {type:"text", text:"..."} or an array of
		// such blocks. Try both shapes.
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
			for _, b := range arr {
				if b.Text != "" {
					return b.Text
				}
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// formatHLCTime renders the wall component of an HLC as a local-
// timezone "2006-01-02 15:04:05.000" string. Falls back to the raw
// HLC representation when the timestamp is zero so we never silently
// emit a meaningless "1970-01-01" line.
func formatHLCTime(h eventlog.HLC) string {
	if h.Wall == 0 {
		return h.String()
	}
	return h.Time().Local().Format("2006-01-02 15:04:05.000")
}
