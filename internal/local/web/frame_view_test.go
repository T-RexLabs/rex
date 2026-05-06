package web

import (
	"strings"
	"testing"
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
