// Package primharness implements the harness_invocation primitive
// from execution.PRIM.1: launch a harness as an ACP server, send a
// prompt, capture every received frame.
//
// In v1 the protocol details a harness uses to signal "I'm done" are
// not yet pinned down, and the per-harness adapter registry
// (execution.ADAPT.*) does not exist yet. So the skeleton is
// intentionally conservative: it sends session/new with the prompt
// and waits until the harness either responds and closes its stdout
// (clean exit), the context is cancelled, or the configured timeout
// elapses. Final-message and tool-call extraction will land once the
// adapter layer pins the wire shape.
package primharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/asabla/rex/internal/core/acp"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/adapter"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "harness_invocation"

// Config is the JSON shape stored in Node.Config.
type Config struct {
	// Harness names a registered adapter (execution.ADAPT.*); when
	// set, primharness looks the adapter up via the registry and
	// asks it to build the *exec.Cmd. Mutually exclusive with
	// Command — supplying both is a configuration error.
	Harness string `json:"harness,omitempty"`
	// Command is the literal argv used to launch the harness as an
	// ACP server on stdio. Used when no adapter exists for the
	// harness yet (development) or when the caller needs to drive
	// a custom binary outside the registry.
	Command []string `json:"command,omitempty"`
	// Dir overrides the working directory of the harness.
	Dir string `json:"dir,omitempty"`
	// Env adds (or overrides) environment entries.
	Env map[string]string `json:"env,omitempty"`
	// Prompt is the initial user message handed to session/new.
	Prompt string `json:"prompt"`
	// Model and Mode pass through to session/new (execution.ACP.2 —
	// model and mode switching).
	Model string `json:"model,omitempty"`
	Mode  string `json:"mode,omitempty"`
	// MCPServers are attached directly per execution.ACP.5; no portal
	// proxy intervenes in v1 (overview.SCOPE).
	MCPServers []acp.MCPServer `json:"mcp_servers,omitempty"`
	// Timeout bounds the entire invocation, end-to-end. Zero means no
	// timeout — the harness is trusted to exit on its own.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Output is what the primitive returns. Final assistant message and
// the tool-call list will be added once the ACP frame schema is
// pinned (overview.SYS.4 — additive evolution).
type Output struct {
	SessionID  string        `json:"session_id"`
	FrameCount int           `json:"frame_count"`
	Duration   time.Duration `json:"duration_ns"`
	ExitCode   int           `json:"exit_code"`
}

// Options configure New. WorkspaceID flows into session/new params.
// OnFrame is the per-frame transcript-capture hook the executor
// supplies (execution.ACP.3); its default is a no-op so the primitive
// is usable in unit tests that do not persist transcripts.
type Options struct {
	WorkspaceID  string
	OnFrame      acp.FrameObserver
	OnPermission acp.PermissionHandler

	// Adapters is the registry consulted when Config.Harness is set.
	// Nil means "use adapter.Default()", which is what production
	// callers want; tests pass a custom registry to stay isolated.
	Adapters *adapter.Registry
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
			return runner.PrimitiveOutput{}, fmt.Errorf("primharness: decode config: %w", err)
		}
	}
	if cfg.Harness != "" && len(cfg.Command) > 0 {
		return runner.PrimitiveOutput{}, errors.New("primharness: harness and command are mutually exclusive")
	}
	if cfg.Harness == "" && len(cfg.Command) == 0 {
		return runner.PrimitiveOutput{}, errors.New("primharness: harness or command is required")
	}
	if cfg.Prompt == "" {
		return runner.PrimitiveOutput{}, errors.New("primharness: prompt is required")
	}

	cmdCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	cmd, err := buildCmd(cmdCtx, opts, cfg)
	if err != nil {
		return runner.PrimitiveOutput{}, err
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: stderr pipe: %w", err)
	}

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: start: %w", err)
	}
	go drainStderr(stderr) // discard but consume so the harness does not block

	var frameCount atomic.Int32
	observer := func(raw acp.RawMessage) {
		frameCount.Add(1)
		if opts.OnFrame != nil {
			opts.OnFrame(raw)
		}
	}

	client, err := acp.New(acp.Config{
		Recv:         stdout,
		Send:         stdin,
		OnFrame:      observer,
		OnPermission: opts.OnPermission,
		// Closing stdin signals EOF to the harness so it exits.
		Close: func() error { return stdin.Close() },
	})
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: build client: %w", err)
	}

	// session/new carries the prompt directly so the harness has
	// everything it needs to start work; the response gives us the
	// session id and signals the harness has begun streaming updates.
	res, err := client.NewSession(cmdCtx, acp.SessionNewParams{
		WorkspaceID: opts.WorkspaceID,
		Prompt:      cfg.Prompt,
		Model:       cfg.Model,
		Mode:        cfg.Mode,
		MCPServers:  cfg.MCPServers,
	})
	if err != nil {
		_ = client.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: session/new: %w", err)
	}

	// Wait for the harness to close its stdout (signalling end of
	// session output) or for ctx/timeout to fire. Until the adapter
	// layer pins down a "session/done" notification, EOF is the
	// canonical end-of-session marker.
	select {
	case <-client.Done():
	case <-cmdCtx.Done():
		// Cancel via the protocol first, then close transports.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = client.CancelSession(cancelCtx, res.SessionID)
		cancel()
	}
	_ = client.Close()

	waitErr := cmd.Wait()
	duration := time.Since(startedAt)

	exitCode := 0
	if waitErr != nil {
		if cfg.Timeout > 0 && errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return runner.PrimitiveOutput{}, fmt.Errorf("primharness: harness timed out after %s", cfg.Timeout)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return runner.PrimitiveOutput{}, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return runner.PrimitiveOutput{}, fmt.Errorf("primharness: wait: %w", waitErr)
		}
	}

	out := Output{
		SessionID:  res.SessionID,
		FrameCount: int(frameCount.Load()),
		Duration:   duration,
		ExitCode:   exitCode,
	}
	body, err := json.Marshal(out)
	if err != nil {
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: marshal output: %w", err)
	}
	if exitCode != 0 {
		return runner.PrimitiveOutput{Output: body}, fmt.Errorf("primharness: harness exit %d", exitCode)
	}
	return runner.PrimitiveOutput{Output: body}, nil
}

func drainStderr(r io.Reader) {
	_, _ = io.Copy(io.Discard, r)
}

// buildCmd resolves cfg.Harness to a registered adapter and asks it
// to build the *exec.Cmd, or falls back to spawning cfg.Command
// directly when no harness name is given.
func buildCmd(ctx context.Context, opts Options, cfg Config) (*exec.Cmd, error) {
	if cfg.Harness != "" {
		reg := opts.Adapters
		if reg == nil {
			reg = adapter.Default()
		}
		a, ok := reg.Lookup(cfg.Harness)
		if !ok {
			return nil, fmt.Errorf("primharness: no adapter registered for harness %q", cfg.Harness)
		}
		cmd, err := a.Spawn(adapter.SpawnOptions{
			Ctx:   ctx,
			Dir:   cfg.Dir,
			Env:   cfg.Env,
			Model: cfg.Model,
			Mode:  cfg.Mode,
		})
		if err != nil {
			return nil, fmt.Errorf("primharness: %s spawn: %w", cfg.Harness, err)
		}
		return cmd, nil
	}
	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = mergedEnv(cfg.Env)
	}
	return cmd, nil
}
