package web

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/acp"
	"github.com/asabla/rex/internal/core/runner/adapter"
)

func TestHelperACPServer(t *testing.T) {
	mode := os.Getenv("GO_WANT_ACP_HELPER_MODE")
	if mode == "" {
		return
	}
	r := acp.NewReader(os.Stdin)
	w := acp.NewWriter(os.Stdout)

	newRaw, err := r.Next()
	if err != nil || newRaw.Message.Method != acp.MethodSessionNew {
		os.Exit(0)
	}
	newResp, _ := acp.NewResponse(newRaw.Message.ID, acp.SessionNewResult{SessionID: "web-mock-1"})
	_ = w.Write(newResp)

	promptRaw, err := r.Next()
	if err != nil || promptRaw.Message.Method != acp.MethodSessionPrompt {
		os.Exit(0)
	}
	if mode == "slow" {
		time.Sleep(1200 * time.Millisecond)
	}
	update, _ := acp.NewNotification("session/update", map[string]any{
		"sessionId": "web-mock-1",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "hello from web harness"},
		},
	})
	_ = w.Write(update)
	promptResp, _ := acp.NewResponse(promptRaw.Message.ID, acp.SessionPromptResult{StopReason: "end_turn"})
	_ = w.Write(promptResp)
	os.Exit(0)
}

type testACPAdapter struct{ helperMode string }

func (testACPAdapter) Name() string { return "opencode" }
func (testACPAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Models:      []string{"opencode/big-pickle", "openai/gpt-4.1"},
		Modes:       []string{"build", "plan"},
		SupportsMCP: true,
	}
}
func (a testACPAdapter) Spawn(opts adapter.SpawnOptions) (*exec.Cmd, error) {
	cmd := exec.CommandContext(opts.Ctx, os.Args[0], "-test.run=TestHelperACPServer")
	cmd.Env = append(os.Environ(), "GO_WANT_ACP_HELPER_MODE="+a.helperMode)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	return cmd, nil
}

func testHarnessRegistry(t *testing.T, helperMode string) *adapter.Registry {
	t.Helper()
	reg := adapter.NewRegistry()
	if err := reg.Register(testACPAdapter{helperMode: helperMode}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func waitForBody(t *testing.T, url string, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			last = err.Error()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		body := readBody(t, resp)
		last = body
		matched := true
		for _, s := range want {
			if !strings.Contains(body, s) {
				matched = false
				break
			}
		}
		if matched {
			return body
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %v\n%s", want, last[:minInt(len(last), 4000)])
	return ""
}

func TestRunNewRendersHarnessOptions(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-new")
	reg := adapter.NewRegistry()
	if err := reg.Register(testStaticAdapter{
		name: "opencode",
		caps: adapter.Capabilities{Models: []string{"opencode/big-pickle"}, Modes: []string{"build", "plan"}},
	}); err != nil {
		t.Fatalf("Register opencode: %v", err)
	}
	if err := reg.Register(testStaticAdapter{
		name: "codex",
		caps: adapter.Capabilities{Models: []string{"gpt-5-codex"}},
	}); err != nil {
		t.Fatalf("Register codex: %v", err)
	}
	if err := reg.Register(testStaticAdapter{name: "claude-code"}); err != nil {
		t.Fatalf("Register claude-code: %v", err)
	}
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: reg})

	resp, err := http.Get(hs.URL + "/runs/new")
	if err != nil {
		t.Fatalf("GET /runs/new: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"run type", "harness", "prompt", "interaction loop", ">opencode<", ">codex<", ">claude-code<", "run-form.js", "opencode/big-pickle", "gpt-5-codex", ">build<", ">plan<"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in /runs/new\n%s", want, body[:minInt(len(body), 3000)])
		}
	}
	for _, want := range []string{`id="run-shell-panel"`, `data-run-panel="harness"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in /runs/new\n%s", want, body[:minInt(len(body), 3000)])
		}
	}
}

func TestRunStartHarnessRedirectsImmediately(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-harness-redirect")
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: testHarnessRegistry(t, "slow")})

	form := strings.NewReader("run_type=harness&harness=opencode&prompt=hello")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	started := time.Now()
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST /runs/start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body := readBody(t, resp)
		t.Fatalf("status: %d\n%s", resp.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("redirect took too long: %s", elapsed)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/runs/") {
		t.Fatalf("Location = %q, want /runs/<id>", loc)
	}
	resp2, err := http.Get(hs.URL + loc)
	if err != nil {
		t.Fatalf("GET redirected run page: %v", err)
	}
	body2 := readBody(t, resp2)
	if !strings.Contains(body2, "events") {
		t.Errorf("expected run detail page\n%s", body2[:minInt(len(body2), 3000)])
	}
}

func TestRunStartHarnessFollowRedirectShowsDetail(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-harness")
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: testHarnessRegistry(t, "fast")})

	form := strings.NewReader("run_type=harness&harness=opencode&prompt=" + urlEncode("say hello") + "&model=" + urlEncode("openai/gpt-4.1") + "&mode=plan")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST /runs/start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body := readBody(t, resp)
		t.Fatalf("status: %d\n%s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/runs/") {
		t.Fatalf("Location = %q, want /runs/<id>", loc)
	}
	body := waitForBody(t, hs.URL+loc, "pill-completed", "hello from web harness", "show raw frames (debug)", "events")
	_ = body

	_ = waitForBody(t, hs.URL+"/runs", "pill-harness")
}

func TestRunStartHarnessRequiresPrompt(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-harness-prompt")
	hs := newTestServerWithOptions(t, Options{WorkspaceRoot: root, Adapters: testHarnessRegistry(t, "fast")})

	form := strings.NewReader("run_type=harness&harness=opencode&mode=plan")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST /runs/start: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	for _, want := range []string{"prompt is required", "id=\"run-harness\"", "value=\"plan\" selected"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q\n%s", want, body[:minInt(len(body), 3000)])
		}
	}
}
