package opencode

import (
	"context"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

func TestNameIsKebabCase(t *testing.T) {
	t.Parallel()
	if Name != "opencode" {
		t.Errorf("Name = %q, want %q", Name, "opencode")
	}
	if (Adapter{}).Name() != Name {
		t.Errorf("Adapter.Name() = %q, want %q", (Adapter{}).Name(), Name)
	}
}

func TestCapabilitiesAdvertisesMCP(t *testing.T) {
	t.Parallel()
	caps := (Adapter{}).Capabilities()
	if !caps.SupportsMCP {
		t.Error("expected SupportsMCP=true")
	}
	if got := strings.Join(caps.Models, ","); !strings.Contains(got, "opencode/big-pickle") {
		t.Errorf("expected curated OpenCode model list, got %v", caps.Models)
	}
	if got := strings.Join(caps.Modes, ","); got != "build,plan" {
		t.Errorf("Modes = %q, want %q", got, "build,plan")
	}
}

func TestSpawnRequiresContext(t *testing.T) {
	t.Parallel()
	if _, err := (Adapter{}).Spawn(adapter.SpawnOptions{}); err == nil {
		t.Fatal("expected error when Ctx is nil")
	}
}

func TestSpawnUsesOpencodeACPByDefault(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{
		Ctx: ctx, Dir: "/tmp", Model: "openai/gpt-4.1", Mode: "build",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := cmd.Args[0]; got != "opencode" {
		t.Errorf("cmd.Args[0] = %q, want opencode", got)
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("cmd.Dir = %q, want /tmp", cmd.Dir)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"acp", "--cwd /tmp", "--model openai/gpt-4.1", "--agent build"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in args, got %q", want, joined)
		}
	}
}

func TestSpawnHonoursBinaryOverride(t *testing.T) {
	t.Setenv(envBinaryOverride, "/usr/local/bin/opencode-dev")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{Ctx: ctx})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := cmd.Args[0]; got != "/usr/local/bin/opencode-dev" {
		t.Errorf("cmd.Args[0] = %q, want override binary", got)
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

func TestParseModelList(t *testing.T) {
	t.Parallel()
	got := parseModelList([]byte("alpha\n\n beta \nalpha\n"))
	if strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("parseModelList = %v, want [alpha beta]", got)
	}
}
