package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
)

// frameEvent is a tiny helper to build a runner.HarnessFrameEvent
// payload from a raw inner ACP frame body and method.
func frameEvent(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	innerParams, _ := json.Marshal(params)
	frame, _ := json.Marshal(map[string]any{
		"method": method,
		"params": json.RawMessage(innerParams),
	})
	ev := runner.HarnessFrameEvent{
		Method: method,
		Frame:  frame,
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func TestSummarizeHarnessFrameAgentMessageChunkText(t *testing.T) {
	t.Parallel()
	p := frameEvent(t, "session/update", map[string]any{
		"sessionId": "s1",
		"update": map[string]any{
			"type":    "agent_message_chunk",
			"content": map[string]any{"type": "text", "text": "Hello world"},
		},
	})
	got := summarizeHarnessFrame(p)
	if !strings.Contains(got, "agent_message_chunk") {
		t.Errorf("missing update type in: %q", got)
	}
	if !strings.Contains(got, "Hello world") {
		t.Errorf("missing extracted text in: %q", got)
	}
}

func TestSummarizeHarnessFrameToolCall(t *testing.T) {
	t.Parallel()
	p := frameEvent(t, "session/update", map[string]any{
		"update": map[string]any{
			"type": "tool_call",
			"tool": map[string]any{"name": "read_file"},
		},
	})
	got := summarizeHarnessFrame(p)
	if !strings.Contains(got, "tool_call") || !strings.Contains(got, "read_file") {
		t.Errorf("expected tool_call + read_file in: %q", got)
	}
}

func TestSummarizeHarnessFrameTruncatesLongText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 200)
	p := frameEvent(t, "session/update", map[string]any{
		"update": map[string]any{
			"type":    "agent_message_chunk",
			"content": map[string]any{"type": "text", "text": long},
		},
	})
	got := summarizeHarnessFrame(p)
	if len(got) > 120 {
		t.Errorf("expected truncation; got %d chars: %q", len(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis: %q", got)
	}
}

func TestSummarizeHarnessFrameContentArray(t *testing.T) {
	t.Parallel()
	p := frameEvent(t, "session/update", map[string]any{
		"update": map[string]any{
			"type": "agent_message_chunk",
			"content": []map[string]any{
				{"type": "text", "text": "first"},
			},
		},
	})
	got := summarizeHarnessFrame(p)
	if !strings.Contains(got, "first") {
		t.Errorf("expected text from content array: %q", got)
	}
}

func TestSummarizeHarnessFrameMethodOnly(t *testing.T) {
	t.Parallel()
	// A response frame with no params and no result text — just a
	// method on the envelope. Should fall back to the method name.
	ev := runner.HarnessFrameEvent{
		Method: "session/new",
		Frame:  json.RawMessage(`{"id":1,"result":{"sessionId":"s"}}`),
	}
	p, _ := json.Marshal(ev)
	got := summarizeHarnessFrame(p)
	if got != "session/new" {
		t.Errorf("expected 'session/new', got %q", got)
	}
}

// TestSummarizeHarnessFrameSessionUpdateField asserts the
// Anthropic claude-agent-acp wire shape (sessionUpdate as the
// discriminator) is recognized — regression from the original
// release that only read `type`.
func TestSummarizeHarnessFrameSessionUpdateField(t *testing.T) {
	t.Parallel()
	p := frameEvent(t, "session/update", map[string]any{
		"sessionId": "s1",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "OK"},
		},
	})
	got := summarizeHarnessFrame(p)
	if !strings.Contains(got, "agent_message_chunk") {
		t.Errorf("missing kind: %q", got)
	}
	if !strings.Contains(got, "OK") {
		t.Errorf("missing text: %q", got)
	}
}

// TestSummarizeHarnessFrameUsageUpdate asserts the Claude-specific
// usage_update meter renders as a useful one-liner instead of
// just the raw kind name.
func TestSummarizeHarnessFrameUsageUpdate(t *testing.T) {
	t.Parallel()
	p := frameEvent(t, "session/update", map[string]any{
		"update": map[string]any{
			"sessionUpdate": "usage_update",
			"used":          30265,
			"size":          200000,
		},
	})
	got := summarizeHarnessFrame(p)
	if !strings.Contains(got, "30265/200000") {
		t.Errorf("expected token meter, got %q", got)
	}
}
