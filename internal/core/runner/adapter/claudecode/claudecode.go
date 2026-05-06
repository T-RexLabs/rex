// Package claudecode is the Claude Code harness adapter
// (execution.ADAPT.2). Claude Code speaks ACP via the upstream
// `@agentclientprotocol/claude-agent-acp` bridge; the adapter spawns
// it through `npx` so users don't have to do a global install.
//
// The adapter is intentionally minimal — env passthrough, working-
// directory propagation, model/mode flags. Capability negotiation
// (e.g. which model names the bridge actually supports) is the
// harness's job; the adapter just plumbs the strings through.
package claudecode

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

// Name is the registry key used by `rex run start --harness <name>`.
const Name = "claude-code"

// envBridgeOverride lets operators point the adapter at a forked or
// pinned ACP bridge without rebuilding rex; useful in CI and for
// reproducing user issues against a known-good bridge revision.
const envBridgeOverride = "REX_CLAUDE_CODE_BRIDGE"

// defaultBridge is the upstream npm package; npx fetches+caches the
// latest matching tag on first use.
const defaultBridge = "@agentclientprotocol/claude-agent-acp"

// Adapter implements adapter.Adapter for Claude Code.
type Adapter struct{}

// Name reports the registry key.
func (Adapter) Name() string { return Name }

// Capabilities advertises Claude Code's surface to the runner.
// Models are left empty (= "any") because the upstream bridge
// proxies whatever Anthropic exposes through the user's auth; the
// list rotates faster than the adapter can usefully encode. MCP
// servers are supported natively (execution.ACP.5).
func (Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Models: []string{
			"sonnet",
			"opus",
			"haiku",
			"claude-sonnet-4-5",
			"claude-opus-4-1",
		},
		Modes:       nil,
		SupportsMCP: true,
	}
}

// Spawn builds the *exec.Cmd that runs the Claude Code ACP bridge on
// stdio. The caller (primharness) wires stdin/stdout/stderr and runs
// Start/Wait.
func (Adapter) Spawn(opts adapter.SpawnOptions) (*exec.Cmd, error) {
	if opts.Ctx == nil {
		return nil, fmt.Errorf("claudecode: SpawnOptions.Ctx is required")
	}

	bridge := defaultBridge
	if v := os.Getenv(envBridgeOverride); v != "" {
		bridge = v
	}
	args := []string{"--yes", bridge}
	if opts.Mode != "" {
		args = append(args, "--mode", opts.Mode)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	cmd := exec.CommandContext(opts.Ctx, "npx", args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = mergedEnv(opts.Env)
	return cmd, nil
}

// mergedEnv overlays opts.Env on top of os.Environ() so callers can
// override individual keys (ANTHROPIC_API_KEY, NODE_OPTIONS, etc.)
// without losing PATH or HOME.
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

// indexByte avoids pulling in the strings package for a one-byte
// scan; primharness aims for low import overhead.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// Register installs the adapter in r. Adapter packages typically
// call this from the binary's main() during startup so unit tests
// can build their own registries without side-effecting the global.
func Register(r *adapter.Registry) error { return r.Register(Adapter{}) }

// init registers with the package-default registry so callers that
// import this package get the adapter without an extra Register
// call. Tests using NewRegistry() are unaffected.
func init() {
	if err := Register(adapter.Default()); err != nil {
		// MustRegister-style: a duplicate at init time is a
		// programming error.
		panic(err)
	}
}
