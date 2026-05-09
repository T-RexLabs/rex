package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedToolsWorkspace stands up a minimal workspace with a
// couple of specs so the tools have something to read.
func seedToolsWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".rex", "workspace.yaml"),
		[]byte("id: tw\nname: Tools workspace\n"), 0o644); err != nil {
		t.Fatalf("write workspace.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.yaml"),
		[]byte("spec_version: 1\nmetadata: {id: alpha, name: Alpha, state: draft}\n"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.yaml"),
		[]byte("spec_version: 1\nmetadata: {id: beta, name: Beta, state: draft}\n"), 0o644); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	return root
}

// callTool registers the workspace tools, dispatches a single
// tools/call by name, and returns the textual content from the
// first content block.
func callTool(t *testing.T, root, name string, args map[string]any) string {
	t.Helper()
	srv := New(ServerInfo{Name: "rex", Version: "test"})
	WorkspaceTools(srv, root)
	srv.mu.Lock()
	handler := srv.handlers[name]
	srv.mu.Unlock()
	if handler == nil {
		t.Fatalf("tool %q not registered", name)
	}
	body, _ := json.Marshal(args)
	res, err := handler(context.Background(), body)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

// TestRexSpecListReturnsAllSpecs covers the spec-enumeration
// tool. The output is Markdown bullets with id + name + state.
func TestRexSpecListReturnsAllSpecs(t *testing.T) {
	t.Parallel()
	root := seedToolsWorkspace(t)
	out := callTool(t, root, "rex.spec.list", nil)
	for _, want := range []string{"`alpha`", "Alpha", "`beta`", "Beta", "draft"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in list output:\n%s", want, out)
		}
	}
}

// TestRexSpecReadReturnsBody covers the read-by-id tool.
func TestRexSpecReadReturnsBody(t *testing.T) {
	t.Parallel()
	root := seedToolsWorkspace(t)
	out := callTool(t, root, "rex.spec.read", map[string]any{"id": "alpha"})
	if !strings.Contains(out, "id: alpha") {
		t.Fatalf("missing id field in read output:\n%s", out)
	}
	if !strings.Contains(out, "name: Alpha") {
		t.Fatalf("missing name field in read output:\n%s", out)
	}
}

// TestRexSpecReadRejectsMissingID covers the no-such-spec
// path: the tool returns an error result rather than panicking
// or returning empty content.
func TestRexSpecReadRejectsMissingID(t *testing.T) {
	t.Parallel()
	root := seedToolsWorkspace(t)
	srv := New(ServerInfo{Name: "rex", Version: "test"})
	WorkspaceTools(srv, root)
	srv.mu.Lock()
	handler := srv.handlers["rex.spec.read"]
	srv.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"id": "ghost"})
	res, _ := handler(context.Background(), body)
	if !res.IsError {
		t.Fatalf("expected isError, got: %+v", res)
	}
}

// TestRexWorkspaceBriefReturnsPrimer reuses the harnessbrief
// renderer; this test confirms the tool surface is wired to it
// rather than producing an empty string.
func TestRexWorkspaceBriefReturnsPrimer(t *testing.T) {
	t.Parallel()
	root := seedToolsWorkspace(t)
	out := callTool(t, root, "rex.workspace.brief", nil)
	for _, want := range []string{"Rex workspace context", "Tools workspace", "Active specs (2)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in brief:\n%s", want, out)
		}
	}
}

// TestRexSpecValidateReportsCleanWorkspace confirms the
// validator-tool returns the expected "0 errors" summary on a
// well-formed workspace.
func TestRexSpecValidateReportsCleanWorkspace(t *testing.T) {
	t.Parallel()
	root := seedToolsWorkspace(t)
	out := callTool(t, root, "rex.spec.validate", nil)
	if !strings.Contains(out, "0 errors") {
		t.Fatalf("expected 0-error summary: %s", out)
	}
}

// TestRexEventsRecentEmptyLog reports the placeholder when
// events.log doesn't exist yet — typical for a fresh
// workspace.
func TestRexEventsRecentEmptyLog(t *testing.T) {
	t.Parallel()
	root := seedToolsWorkspace(t)
	out := callTool(t, root, "rex.events.recent", nil)
	if !strings.Contains(out, "events.log") && !strings.Contains(out, "no events") {
		t.Fatalf("expected fresh-workspace placeholder: %s", out)
	}
}
