package primharness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/acp"
	"github.com/asabla/rex/internal/core/runner"
)

// TestMain doubles as a mock ACP harness when invoked with the
// REX_TEST_HARNESS_MODE env var. The standard Go-test-as-subprocess
// trick lets primharness drive a real exec.Cmd in tests without
// needing a separate binary checked into the repo.
func TestMain(m *testing.M) {
	switch os.Getenv("REX_TEST_HARNESS_MODE") {
	case "echo":
		runEchoHarness()
		return
	case "slow":
		runSlowHarness()
		return
	case "fail":
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// runEchoHarness pretends to be an ACP server that emits two
// session/update notifications, responds to session/new, then exits.
func runEchoHarness() {
	r := acp.NewReader(os.Stdin)
	w := acp.NewWriter(os.Stdout)

	raw, err := r.Next()
	if err != nil {
		return
	}
	if raw.Message.Method != acp.MethodSessionNew {
		return
	}
	for i := 0; i < 2; i++ {
		n, _ := acp.NewNotification("session/update", map[string]int{"i": i})
		_ = w.Write(n)
	}
	resp, _ := acp.NewResponse(raw.Message.ID, acp.SessionNewResult{SessionID: "mock-1"})
	_ = w.Write(resp)
	// Returning closes stdout, which signals end-of-session to the
	// client.
}

// runSlowHarness ignores the prompt and never responds, so the
// primitive should hit its timeout. It must read stdin to keep the
// pipe drained, otherwise primharness would block on its own write.
func runSlowHarness() {
	r := acp.NewReader(os.Stdin)
	for {
		_, err := r.Next()
		if err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func runPrim(t *testing.T, cfg Config, opts Options) (Output, error) {
	t.Helper()
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	prim := New(opts)
	res, primErr := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "x", Type: PrimitiveType, Config: cfgBytes},
	})
	var out Output
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
	}
	return out, primErr
}

func TestHarnessInvocationCapturesFramesAndCompletes(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		frames  []string
	)
	observer := func(raw acp.RawMessage) {
		mu.Lock()
		frames = append(frames, raw.Message.Method+":"+raw.Message.Method)
		_ = raw // silence linter on alternate paths
		mu.Unlock()
	}

	cfg := Config{
		Command: []string{os.Args[0]},
		Env:     map[string]string{"REX_TEST_HARNESS_MODE": "echo"},
		Prompt:  "hi",
	}
	out, err := runPrim(t, cfg, Options{
		WorkspaceID: "ws-test",
		OnFrame:     observer,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.SessionID != "mock-1" {
		t.Fatalf("session: got %q", out.SessionID)
	}
	if out.FrameCount != 3 {
		t.Fatalf("frame count: got %d want 3 (2 updates + 1 response)", out.FrameCount)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit: got %d", out.ExitCode)
	}

	mu.Lock()
	got := len(frames)
	mu.Unlock()
	if got != 3 {
		t.Fatalf("observer saw %d frames, want 3", got)
	}
}

func TestHarnessInvocationRequiresCommand(t *testing.T) {
	t.Parallel()

	_, err := runPrim(t, Config{Prompt: "x"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("err: got %v want command-required error", err)
	}
}

func TestHarnessInvocationRequiresPrompt(t *testing.T) {
	t.Parallel()

	_, err := runPrim(t, Config{Command: []string{"true"}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("err: got %v want prompt-required error", err)
	}
}

func TestHarnessInvocationTimeout(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Command: []string{os.Args[0]},
		Env:     map[string]string{"REX_TEST_HARNESS_MODE": "slow"},
		Prompt:  "hi",
		Timeout: 150 * time.Millisecond,
	}
	_, err := runPrim(t, cfg, Options{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Either the cancel path (session/new returned ctx error) or the
	// post-Wait timeout-detection path produces an error mentioning
	// the deadline; both indicate the timeout fired.
	if !strings.Contains(err.Error(), "timed out") &&
		!strings.Contains(err.Error(), "context deadline") &&
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error not timeout-shaped: %v", err)
	}
}

func TestHarnessInvocationFrameObserverNotRequired(t *testing.T) {
	t.Parallel()

	// No observer installed: primitive must still drive the session
	// and complete cleanly. The internal frame counter still ticks.
	cfg := Config{
		Command: []string{os.Args[0]},
		Env:     map[string]string{"REX_TEST_HARNESS_MODE": "echo"},
		Prompt:  "hi",
	}
	out, err := runPrim(t, cfg, Options{WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.FrameCount != 3 {
		t.Fatalf("frame count: got %d want 3", out.FrameCount)
	}
}

// TestHarnessInvocationFrameObserverConcurrency exercises the observer
// hook under racy access to ensure we are not leaking the frame count
// to multiple writers.
func TestHarnessInvocationFrameObserverConcurrency(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	observer := func(_ acp.RawMessage) { calls.Add(1) }

	cfg := Config{
		Command: []string{os.Args[0]},
		Env:     map[string]string{"REX_TEST_HARNESS_MODE": "echo"},
		Prompt:  "hi",
	}
	if _, err := runPrim(t, cfg, Options{OnFrame: observer}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("observer calls: got %d want 3", calls.Load())
	}
}
