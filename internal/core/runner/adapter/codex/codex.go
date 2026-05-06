// Package codex is the Codex harness adapter (execution.ADAPT.2).
// Codex itself does not expose a native ACP server, so rex drives it
// through the `@zed-industries/codex-acp` bridge on stdio.
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

const Name = "codex"

const (
	envBinaryOverride  = "REX_CODEX_ACP_BIN"
	envPackageOverride = "REX_CODEX_ACP_PACKAGE"
	defaultBinary      = "npx"
	defaultPackage     = "@zed-industries/codex-acp"
)

type Adapter struct{}

func (Adapter) Name() string { return Name }

func (Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Models: []string{
			"gpt-5-codex",
			"gpt-5",
			"gpt-5-mini",
			"gpt-5-nano",
			"o3",
			"o4-mini",
		},
		Modes:       nil,
		SupportsMCP: true,
	}
}

func (Adapter) Spawn(opts adapter.SpawnOptions) (*exec.Cmd, error) {
	if opts.Ctx == nil {
		return nil, fmt.Errorf("codex: SpawnOptions.Ctx is required")
	}
	binary := defaultBinary
	if v := os.Getenv(envBinaryOverride); v != "" {
		binary = v
	}
	args := []string{}
	if binary == defaultBinary {
		pkg := defaultPackage
		if v := os.Getenv(envPackageOverride); v != "" {
			pkg = v
		}
		args = append(args, "--yes", pkg)
	}
	if opts.Model != "" {
		args = append(args, "-c", `model=`+tomlString(opts.Model))
	}
	cmd := exec.CommandContext(opts.Ctx, binary, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = mergedEnv(opts.Env)
	return cmd, nil
}

func tomlString(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}

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

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func Register(r *adapter.Registry) error { return r.Register(Adapter{}) }

func init() {
	if err := Register(adapter.Default()); err != nil {
		panic(err)
	}
}
