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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
	OnInput      func(ctx context.Context, sessionID string) (string, error)

	// Adapters is the registry consulted when Config.Harness is set.
	// Nil means "use adapter.Default()", which is what production
	// callers want; tests pass a custom registry to stay isolated.
	Adapters *adapter.Registry

	// OnStderr is invoked once per line written to the harness's
	// stderr — bridge diagnostics, npx warnings, anything the
	// harness uses for human-readable output. Nil silently drops
	// (the goroutine still consumes the pipe so the harness does
	// not block on a full buffer). The CLI typically wires this
	// to os.Stderr-prefixed; the web UI captures the lines as
	// runner events.
	OnStderr func(line string)
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
	// Forward harness stderr to opts.OnStderr line-by-line so the
	// CLI can surface bridge diagnostics (npx output, bridge crash
	// traces, etc.) instead of silently swallowing them. When no
	// hook is configured the goroutine still drains the pipe so
	// the harness doesn't block on a full buffer.
	go forwardStderr(stderr, opts.OnStderr)

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

	// session/new opens the session; the bridge expects cwd
	// (string) and mcpServers (array, may be empty). Send Dir
	// when set, otherwise inherit the harness process's working
	// directory by resolving cwd lazily.
	cwd := cfg.Dir
	if cwd == "" {
		// os.Getwd() is the same dir os/exec inherits when
		// cmd.Dir is empty; pass it explicitly so the bridge
		// has a defined string rather than receiving "".
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	res, err := client.NewSession(cmdCtx, acp.SessionNewParams{
		Cwd:        cwd,
		MCPServers: cfg.MCPServers,
	})
	if err != nil {
		_ = client.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return runner.PrimitiveOutput{}, fmt.Errorf("primharness: session/new: %w", err)
	}

	currentPrompt := cfg.Prompt
	for {
		// Synthesize a user_message_chunk frame so the run timeline
		// shows the prompt the operator sent. The harness adapter only
		// observes inbound frames (responses + harness-sent
		// notifications); without this, the user's input would never
		// appear in events.log alongside the model's reply. Routed
		// through the local observer so the frame counter ticks
		// whether or not opts.OnFrame is configured.
		if synth := buildUserPromptFrame(res.SessionID, currentPrompt); synth != nil {
			observer(*synth)
		}

		// session/prompt delivers one user message turn. The bridge
		// streams session/update notifications throughout this call
		// (captured via OnFrame above) and returns a stop_reason when
		// the model is done. The call is run in a goroutine so the
		// select below can race it against ctx/timeout cancellation.
		promptDone := make(chan error, 1)
		go func(prompt string) {
			_, err := client.SendPrompt(cmdCtx, acp.SessionPromptParams{
				SessionID: res.SessionID,
				Prompt:    acp.TextPromptBlocks(prompt),
			})
			promptDone <- err
		}(currentPrompt)

		var promptErr error
		select {
		case promptErr = <-promptDone:
		case <-cmdCtx.Done():
			// Cancel via the protocol first, then close transports.
			cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = client.CancelSession(cancelCtx, res.SessionID)
			cancel()
			// Drain promptDone so the goroutine returns.
			<-promptDone
		}
		if promptErr != nil && !errors.Is(cmdCtx.Err(), context.Canceled) && !errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			_ = client.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return runner.PrimitiveOutput{}, fmt.Errorf("primharness: session/prompt: %w", promptErr)
		}

		if opts.OnInput == nil {
			break
		}
		nextPrompt, err := opts.OnInput(cmdCtx, res.SessionID)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			_ = client.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return runner.PrimitiveOutput{}, fmt.Errorf("primharness: await input: %w", err)
		}
		nextPrompt = strings.TrimSpace(nextPrompt)
		if nextPrompt == "" {
			break
		}
		currentPrompt = nextPrompt
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

// buildUserPromptFrame fabricates an ACP-shaped session/update
// notification carrying the user's prompt as a user_message_chunk
// update. This is the only synthetic frame primharness emits;
// every other frame is something the harness actually sent.
// Returning nil silently skips emission (empty prompt, marshal
// failure) — the run still works, the timeline just lacks the
// "user" turn.
func buildUserPromptFrame(sessionID, prompt string) *acp.RawMessage {
	if prompt == "" {
		return nil
	}
	params := map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "user_message_chunk",
			"content":       map[string]any{"type": "text", "text": prompt},
		},
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil
	}
	msg := acp.Message{
		JSONRPC: acp.Version,
		Method:  "session/update",
		Params:  rawParams,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil
	}
	return &acp.RawMessage{Message: msg, Raw: body}
}

// forwardStderr scans r line by line and invokes onLine for each.
// When onLine is nil the lines are still consumed (so the harness
// doesn't block on a full pipe buffer) but discarded. Any read
// error terminates the loop silently — the caller already owns
// the harness lifecycle.
func forwardStderr(r io.Reader, onLine func(string)) {
	if r == nil {
		return
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
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
