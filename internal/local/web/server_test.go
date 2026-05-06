package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

// initWorkspace builds a TempDir workspace shape with the v1
// .rex/ skeleton. Mirrors what `rex workspace init` produces but
// avoids importing the cli package (web is a leaf to cli).
func initWorkspace(t *testing.T, id string) string {
	t.Helper()
	root := t.TempDir()
	rex := filepath.Join(root, ".rex")
	for _, sub := range []string{"specs", "schedules", "templates", "hooks", "drafts"} {
		if err := os.MkdirAll(filepath.Join(rex, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	body := []byte("id: " + id + "\nname: " + id + "\nstate: active\ncreated_at: 2026-05-04T12:00:00Z\n")
	if err := os.WriteFile(filepath.Join(rex, "workspace.yaml"), body, 0o644); err != nil {
		t.Fatalf("workspace.yaml: %v", err)
	}
	return root
}

func newTestServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	return newTestServerWithOptions(t, Options{WorkspaceRoot: root})
}

func newTestServerWithOptions(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	if opts.WorkspaceRoot == "" {
		t.Fatal("newTestServerWithOptions: WorkspaceRoot is required")
	}
	if opts.BindAddr == "" {
		opts.BindAddr = "127.0.0.1:0"
	}
	if opts.Version == "" {
		opts.Version = "test"
	}
	s, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return hs
}

type testStaticAdapter struct {
	name string
	caps adapter.Capabilities
}

func (a testStaticAdapter) Name() string { return a.name }
func (a testStaticAdapter) Capabilities() adapter.Capabilities {
	return a.caps
}
func (a testStaticAdapter) Spawn(adapter.SpawnOptions) (*exec.Cmd, error) {
	return nil, fmt.Errorf("test adapter %q should not be spawned", a.name)
}

func TestNewRequiresWorkspaceRoot(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error on missing WorkspaceRoot")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "static")
	hs := newTestServer(t, root)

	for path, mustContain := range map[string]string{
		"/static/app.css":         "rex local ui",
		"/static/htmx.min.js":     "htmx",
		"/static/htmx-ext-sse.js": "sse",
	} {
		resp, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		if !strings.Contains(strings.ToLower(body), mustContain) {
			t.Fatalf("%s: missing %q in body of length %d", path, mustContain, len(body))
		}
	}
}

func TestHomeRendersWorkspaceOverview(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "smoke-ws")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/: status %d body: %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"smoke-ws",
		"<a class=\"brand\" href=\"/\">rex</a>",
		"workspace",
		"app.css",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body[:minInt(len(body), 1000)])
		}
	}
}

func TestHomeShowsRecentRunsTable(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-runs")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body := readBody(t, resp)
	// With no events.log, the table should be the empty state.
	if !strings.Contains(body, "no runs yet") {
		t.Fatalf("expected empty-runs hint: %s", body)
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-404")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", resp.StatusCode)
	}
}

func TestFooterReportsBindAddr(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "footer")
	s, _ := New(Options{
		WorkspaceRoot: root,
		BindAddr:      "127.0.0.1:9999",
		Version:       "v0.1-test",
	})
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "127.0.0.1:9999") {
		t.Fatalf("footer missing bind addr: %s", body)
	}
	if !strings.Contains(body, "v0.1-test") {
		t.Fatalf("footer missing version: %s", body)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
