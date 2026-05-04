package claudecode

import (
	"context"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

func TestNameIsKebabCase(t *testing.T) {
	t.Parallel()
	if Name != "claude-code" {
		t.Errorf("Name = %q, want %q", Name, "claude-code")
	}
	if (Adapter{}).Name() != Name {
		t.Errorf("Adapter.Name() = %q, want %q", (Adapter{}).Name(), Name)
	}
}

func TestCapabilitiesAdvertisesMCP(t *testing.T) {
	t.Parallel()
	caps := (Adapter{}).Capabilities()
	if !caps.SupportsMCP {
		t.Error("expected SupportsMCP=true (execution.ACP.5)")
	}
	if len(caps.Models) != 0 {
		t.Errorf("Models should be empty (any-model passthrough); got %v", caps.Models)
	}
}

func TestSpawnRequiresContext(t *testing.T) {
	t.Parallel()
	if _, err := (Adapter{}).Spawn(adapter.SpawnOptions{}); err == nil {
		t.Fatal("expected error when Ctx is nil")
	}
}

func TestSpawnUsesNpxByDefault(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{
		Ctx: ctx, Dir: "/tmp", Model: "opus", Mode: "sync",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "npx") && cmd.Args[0] != "npx" {
		t.Errorf("cmd.Args[0] = %q, want npx", cmd.Args[0])
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("cmd.Dir = %q, want /tmp", cmd.Dir)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "@agentclientprotocol/claude-agent-acp") {
		t.Errorf("expected default bridge in args, got %q", joined)
	}
	if !strings.Contains(joined, "--model opus") {
		t.Errorf("expected --model opus in args, got %q", joined)
	}
	if !strings.Contains(joined, "--mode sync") {
		t.Errorf("expected --mode sync in args, got %q", joined)
	}
}

func TestSpawnHonoursBridgeOverride(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel().
	t.Setenv("REX_CLAUDE_CODE_BRIDGE", "/usr/local/lib/my-fork.tgz")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{Ctx: ctx})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "/usr/local/lib/my-fork.tgz") {
		t.Errorf("expected override bridge in args, got %q", joined)
	}
	if strings.Contains(joined, "@agentclientprotocol/claude-agent-acp") {
		t.Errorf("did not expect default bridge when override is set, got %q", joined)
	}
}

func TestRegistersWithDefaultRegistry(t *testing.T) {
	t.Parallel()
	got, ok := adapter.Default().Lookup(Name)
	if !ok {
		t.Fatal("not registered with adapter.Default()")
	}
	if got.Name() != Name {
		t.Errorf("Name() = %q, want %q", got.Name(), Name)
	}
}

func TestEnvOverrideMergesOnTopOfOSEnv(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{
		Ctx: ctx,
		Env: map[string]string{"PATH": "/sentinel"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var sawSentinel, sawDuplicate bool
	for _, kv := range cmd.Env {
		if kv == "PATH=/sentinel" {
			if sawSentinel {
				sawDuplicate = true
			}
			sawSentinel = true
		}
	}
	if !sawSentinel {
		t.Error("expected PATH=/sentinel in cmd.Env")
	}
	if sawDuplicate {
		t.Error("expected single PATH entry; got duplicates")
	}
}
