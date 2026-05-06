// Package opencode is the OpenCode harness adapter
// (execution.ADAPT.2). OpenCode ships a native ACP server surfaced as
// `opencode acp`; the adapter spawns it directly and passes through
// the working directory, model, and agent selection.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

// Name is the registry key used by `rex run start --harness <name>`.
const Name = "opencode"

// envBinaryOverride lets operators point rex at a non-PATH OpenCode
// binary without rebuilding. Useful in CI and for smoke-testing a
// locally-built opencode checkout.
const envBinaryOverride = "REX_OPENCODE_BIN"

// defaultBinary is the expected executable name on PATH.
const defaultBinary = "opencode"

// Adapter implements adapter.Adapter for OpenCode.
type Adapter struct{}

// Name reports the registry key.
func (Adapter) Name() string { return Name }

// Capabilities advertises OpenCode's surface to the runner. Models
// and agents evolve outside rex, so both stay open-ended; MCP
// attachment is supported by the ACP server.
func (Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Models: []string{
			"opencode/big-pickle",
			"opencode/gpt-5-nano",
			"openai/gpt-4.1",
			"github-copilot/claude-sonnet-4.6",
		},
		Modes:       []string{"build", "plan"},
		SupportsMCP: true,
	}
}

// Discover queries the locally-installed OpenCode binary for its
// current model list. Modes stay adapter-defined because OpenCode's
// build/plan surface is stable and already part of the CLI contract.
func (Adapter) Discover(ctx context.Context, opts adapter.DiscoverOptions) (adapter.Capabilities, error) {
	binary := defaultBinary
	if v := os.Getenv(envBinaryOverride); v != "" {
		binary = v
	}
	cmd := exec.CommandContext(ctx, binary, "models", "--pure")
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return adapter.Capabilities{Modes: []string{"build", "plan"}, SupportsMCP: true}, err
	}
	models := parseModelList(out)
	return adapter.Capabilities{
		Models:      models,
		Modes:       []string{"build", "plan"},
		SupportsMCP: true,
	}, nil
}

// Spawn builds the *exec.Cmd that runs `opencode acp` on stdio. Mode
// maps to OpenCode's `--agent` flag; model passes through to
// `--model`. The process cwd and the explicit `--cwd` flag stay in
// sync so the ACP server and the child process agree on the project
// root.
func (Adapter) Spawn(opts adapter.SpawnOptions) (*exec.Cmd, error) {
	if opts.Ctx == nil {
		return nil, fmt.Errorf("opencode: SpawnOptions.Ctx is required")
	}

	binary := defaultBinary
	if v := os.Getenv(envBinaryOverride); v != "" {
		binary = v
	}

	args := []string{"acp"}
	if opts.Dir != "" {
		args = append(args, "--cwd", opts.Dir)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Mode != "" {
		args = append(args, "--agent", opts.Mode)
	}

	cmd := exec.CommandContext(opts.Ctx, binary, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = mergedEnv(opts.Env)
	return cmd, nil
}

// mergedEnv overlays opts.Env on top of os.Environ() so callers can
// override individual keys without losing PATH, HOME, or provider
// credentials.
func mergedEnv(extra map[string]string) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	overridden := make(map[string]bool, len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		eq := indexByte(kv, '=')
		if eq < 0 {
			out = append(out, kv)
			continue
		}
		k := kv[:eq]
		if v, ok := extra[k]; ok {
			out = append(out, k+"="+v)
			overridden[k] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		if overridden[k] {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func parseModelList(out []byte) []string {
	s := bufio.NewScanner(bytes.NewReader(out))
	seen := make(map[string]bool)
	var models []string
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		models = append(models, line)
	}
	return models
}

// indexByte avoids a strings import for a single-byte scan.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// Register installs the adapter in r.
func Register(r *adapter.Registry) error { return r.Register(Adapter{}) }

// init registers with the process-default registry.
func init() {
	if err := Register(adapter.Default()); err != nil {
		panic(err)
	}
}
