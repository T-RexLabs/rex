package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
)

func TestRenderFrameCardHTMLThoughtCarriesFrameTextMarker(t *testing.T) {
	t.Parallel()

	row := runEventRow{ID: "evt-1", Timestamp: "2026-05-06T17:00:00Z"}
	fv := &frameView{Kind: "agent_thought", Role: "assistant", Text: "thinking..."}

	html := renderFrameCardHTML(row, fv)
	if !strings.Contains(html, `frame-thought`) {
		t.Fatalf("expected thought class in frame html: %s", html)
	}
	if !strings.Contains(html, `data-frame-text`) {
		t.Fatalf("expected data-frame-text marker in frame html: %s", html)
	}
}

// TestCategorizeFrameClaudeToolCall verifies tool_call frames from
// Anthropic's claude-agent-acp produce a usable view: the tool name
// resolves from `_meta.claudeCode.toolName` (or `title`), the
// toolCallId is preserved for coalescing, and a Skill subtitle is
// pulled out of rawInput. Without this, every Claude Code tool call
// rendered as "(unnamed)" in the run-detail timeline.
func TestCategorizeFrameClaudeToolCall(t *testing.T) {
	t.Parallel()

	frame := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{` +
		`"_meta":{"claudeCode":{"toolName":"Skill"}},` +
		`"toolCallId":"toolu_xyz",` +
		`"sessionUpdate":"tool_call",` +
		`"rawInput":{"skill":"frontend-design:frontend-design","args":"build a page"},` +
		`"status":"pending","title":"Skill","kind":"other","content":[]}}}`)
	ev := runner.HarnessFrameEvent{Frame: json.RawMessage(frame)}

	fv := categorizeFrame(ev, nil)
	if fv == nil {
		t.Fatalf("expected categorizeFrame to return a view")
	}
	if fv.Kind != "tool_call" {
		t.Fatalf("kind = %q, want tool_call", fv.Kind)
	}
	if fv.ToolName != "Skill" {
		t.Fatalf("ToolName = %q, want Skill", fv.ToolName)
	}
	if fv.ToolCallID != "toolu_xyz" {
		t.Fatalf("ToolCallID = %q", fv.ToolCallID)
	}
	if fv.Subtitle != "frontend-design:frontend-design" {
		t.Fatalf("Subtitle = %q, want frontend-design:frontend-design", fv.Subtitle)
	}
	if fv.Status != "pending" {
		t.Fatalf("Status = %q, want pending", fv.Status)
	}
}

// TestCategorizeFrameClaudeToolUpdateCompleted verifies the final
// tool_call_update with status: completed normalises to "ok" (not
// "error" — the prior implementation misclassified it because
// renderFrameCardHTML treated any non-"ok" string as error).
func TestCategorizeFrameClaudeToolUpdateCompleted(t *testing.T) {
	t.Parallel()

	frame := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{` +
		`"_meta":{"claudeCode":{"toolName":"Skill"}},` +
		`"toolCallId":"toolu_xyz",` +
		`"sessionUpdate":"tool_call_update",` +
		`"status":"completed",` +
		`"rawOutput":"Launching skill: frontend-design:frontend-design",` +
		`"content":[{"type":"content","content":{"type":"text","text":"Launching skill: frontend-design:frontend-design"}}]}}}`)
	ev := runner.HarnessFrameEvent{Frame: json.RawMessage(frame)}

	fv := categorizeFrame(ev, nil)
	if fv == nil {
		t.Fatalf("expected categorizeFrame to return a view")
	}
	if fv.Kind != "tool_result" {
		t.Fatalf("kind = %q, want tool_result", fv.Kind)
	}
	if fv.Status != "ok" {
		t.Fatalf("Status = %q, want ok (completed should normalise to ok)", fv.Status)
	}
	if !strings.Contains(fv.ToolOutput, "Launching skill") {
		t.Fatalf("ToolOutput missing content[].text: %q", fv.ToolOutput)
	}
}
