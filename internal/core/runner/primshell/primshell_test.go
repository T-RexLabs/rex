package primshell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
)

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

func TestShellExecSuccess(t *testing.T) {
	t.Parallel()

	out, err := runPrim(t, Config{Command: []string{"echo", "hello"}}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit: got %d", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "hello") {
		t.Fatalf("stdout: got %q", out.Stdout)
	}
	if out.Duration <= 0 {
		t.Fatalf("duration: got %v", out.Duration)
	}
}

func TestShellExecNonZeroIsError(t *testing.T) {
	t.Parallel()

	out, err := runPrim(t, Config{Command: []string{"sh", "-c", "exit 7"}}, Options{})
	if err == nil {
		t.Fatal("Run: want error on non-zero exit")
	}
	if out.ExitCode != 7 {
		t.Fatalf("exit: got %d want 7", out.ExitCode)
	}
}

func TestShellExecCapturesStderr(t *testing.T) {
	t.Parallel()

	out, _ := runPrim(t, Config{Command: []string{"sh", "-c", "echo err >&2; echo out"}}, Options{})
	if !strings.Contains(out.Stdout, "out") {
		t.Fatalf("stdout: got %q", out.Stdout)
	}
	if !strings.Contains(out.Stderr, "err") {
		t.Fatalf("stderr: got %q", out.Stderr)
	}
}

func TestShellExecTruncatesOutput(t *testing.T) {
	t.Parallel()

	out, err := runPrim(t, Config{
		Command:        []string{"sh", "-c", "head -c 4096 /dev/zero | tr '\\0' 'a'"},
		MaxOutputBytes: 100,
	}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.StdoutTrunc {
		t.Fatalf("expected truncation, got full %d bytes", len(out.Stdout))
	}
	if !strings.Contains(out.Stdout, "[truncated") {
		t.Fatalf("missing truncation marker: %q", out.Stdout)
	}
}

func TestShellExecRequiresCommand(t *testing.T) {
	t.Parallel()

	_, err := runPrim(t, Config{}, Options{})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestShellExecHonoursTimeout(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Command: []string{"sh", "-c", "sleep 5"},
		Timeout: 80 * time.Millisecond,
	}
	_, err := runPrim(t, cfg, Options{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestShellExecHonoursDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := Config{Command: []string{"sh", "-c", "pwd"}}
	out, err := runPrim(t, cfg, Options{WorkspaceDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.Stdout, dir) {
		t.Fatalf("pwd: got %q want to contain %q", out.Stdout, dir)
	}
}

func TestShellExecConfigDirOverridesWorkspace(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	overrideDir := t.TempDir()
	cfg := Config{Command: []string{"sh", "-c", "pwd"}, Dir: overrideDir}
	out, err := runPrim(t, cfg, Options{WorkspaceDir: wsDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.Stdout, overrideDir) || strings.Contains(out.Stdout, wsDir) {
		t.Fatalf("override dir not honoured: got %q", out.Stdout)
	}
}
