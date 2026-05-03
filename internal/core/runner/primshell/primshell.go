// Package primshell implements the shell_exec primitive defined in
// execution.PRIM.2: run a shell command in the workspace's working
// directory; output is exit code, stdout, and stderr.
//
// Stdout and stderr are bounded by Config.MaxOutputBytes so a chatty
// command does not blow runner memory; the captured slice is truncated
// with a "[truncated, N more bytes]" marker.
package primshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/asabla/rex/internal/core/runner"
)

// PrimitiveType is the Node.Type string this primitive registers
// under. Locked here so the runner and the spec format agree on one
// canonical name.
const PrimitiveType = "shell_exec"

// DefaultMaxOutputBytes caps stdout/stderr capture per stream. 1 MiB
// is generous for command-line UX and keeps event-log records well
// under storage.MaxRecordSize.
const DefaultMaxOutputBytes = 1 << 20

// Config is the JSON shape stored in Node.Config.
type Config struct {
	// Command is the argv. The first element must be an absolute path
	// or resolvable on PATH; no shell interpolation is applied. To
	// run a shell pipeline, put "sh" / "-c" / "<pipeline>" in here.
	Command []string `json:"command"`
	// Dir overrides the working directory. When empty, the runner's
	// configured workspace dir applies.
	Dir string `json:"dir,omitempty"`
	// Env adds (or overrides, when keys collide) entries on top of
	// the parent process environment.
	Env map[string]string `json:"env,omitempty"`
	// Timeout bounds the command. Zero = no timeout.
	Timeout time.Duration `json:"timeout,omitempty"`
	// MaxOutputBytes caps each captured stream. Zero falls back to
	// DefaultMaxOutputBytes; set to a negative value to capture all.
	MaxOutputBytes int `json:"max_output_bytes,omitempty"`
}

// Output is what the primitive returns in PrimitiveOutput.Output. The
// shape is deterministic so downstream Nodes can branch on ExitCode.
type Output struct {
	ExitCode    int           `json:"exit_code"`
	Stdout      string        `json:"stdout"`
	Stderr      string        `json:"stderr"`
	StdoutTrunc bool          `json:"stdout_truncated,omitempty"`
	StderrTrunc bool          `json:"stderr_truncated,omitempty"`
	Duration    time.Duration `json:"duration_ns"`
}

// Options configure New. WorkspaceDir is the default Dir when a Node's
// Config does not set its own.
type Options struct {
	WorkspaceDir string
}

// New returns a Primitive bound to opts.
func New(opts Options) runner.Primitive {
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return run(ctx, opts, in)
	})
}

func run(ctx context.Context, opts Options, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
	var cfg Config
	if len(in.Node.Config) > 0 {
		if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primshell: decode config: %w", err)
		}
	}
	if len(cfg.Command) == 0 {
		return runner.PrimitiveOutput{}, errors.New("primshell: command is required")
	}
	dir := cfg.Dir
	if dir == "" {
		dir = opts.WorkspaceDir
	}
	maxBytes := cfg.MaxOutputBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxOutputBytes
	}

	cmdCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(cmdCtx, cfg.Command[0], cfg.Command[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = mergeEnv(cfg.Env)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primshell: stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primshell: stderr pipe: %w", err)
	}

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primshell: start: %w", err)
	}

	stdoutCap := newCapture(maxBytes)
	stderrCap := newCapture(maxBytes)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdoutCap, stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stderrCap, stderrPipe)
	}()
	wg.Wait()

	waitErr := cmd.Wait()
	duration := time.Since(startedAt)

	exitCode := 0
	if waitErr != nil {
		// Check ctx state first: a deadline-killed process surfaces
		// as ExitError with exit -1, which would otherwise look
		// indistinguishable from a normal failed command.
		if cfg.Timeout > 0 && errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return runner.PrimitiveOutput{}, fmt.Errorf("primshell: command timed out after %s", cfg.Timeout)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return runner.PrimitiveOutput{}, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return runner.PrimitiveOutput{}, fmt.Errorf("primshell: wait: %w", waitErr)
		}
	}

	out := Output{
		ExitCode:    exitCode,
		Stdout:      string(stdoutCap.bytes()),
		Stderr:      string(stderrCap.bytes()),
		StdoutTrunc: stdoutCap.truncated,
		StderrTrunc: stderrCap.truncated,
		Duration:    duration,
	}
	body, err := json.Marshal(out)
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primshell: marshal output: %w", err)
	}
	if exitCode != 0 {
		// A non-zero exit is a primitive failure so retry/abort
		// machinery applies. Successful runs return nil.
		return runner.PrimitiveOutput{Output: body}, fmt.Errorf("primshell: exit %d", exitCode)
	}
	return runner.PrimitiveOutput{Output: body}, nil
}

// capture is a bounded byte sink: writes past max are dropped and the
// truncated flag is set. Negative max means unbounded.
type capture struct {
	max       int
	buf       []byte
	overflow  int
	truncated bool
}

func newCapture(max int) *capture { return &capture{max: max} }

func (c *capture) Write(p []byte) (int, error) {
	if c.max < 0 || len(c.buf)+len(p) <= c.max {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	room := c.max - len(c.buf)
	if room > 0 {
		c.buf = append(c.buf, p[:room]...)
	}
	c.overflow += len(p) - room
	c.truncated = true
	return len(p), nil
}

func (c *capture) bytes() []byte {
	if !c.truncated {
		return c.buf
	}
	suffix := []byte(fmt.Sprintf("\n[truncated, %d more bytes]\n", c.overflow))
	out := make([]byte, 0, len(c.buf)+len(suffix))
	out = append(out, c.buf...)
	out = append(out, suffix...)
	return out
}

// mergeEnv overlays extra on top of os.Environ-equivalent entries.
// Implementation imports os locally to keep the capture/Output structs
// usable in tests that do not need the env.
func mergeEnv(extra map[string]string) []string {
	out := osEnviron()
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
