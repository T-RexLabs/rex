package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

func TestNameIsKebabCase(t *testing.T) {
	t.Parallel()
	if Name != "codex" {
		t.Errorf("Name = %q, want %q", Name, "codex")
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
	if got := strings.Join(caps.Models, ","); !strings.Contains(got, "gpt-5-codex") {
		t.Errorf("expected curated Codex model list, got %v", caps.Models)
	}
}

func TestSpawnRequiresContext(t *testing.T) {
	t.Parallel()
	if _, err := (Adapter{}).Spawn(adapter.SpawnOptions{}); err == nil {
		t.Fatal("expected error when Ctx is nil")
	}
}

func TestSpawnUsesCodexACPByDefault(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{Ctx: ctx, Model: "gpt-5-codex"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"npx", "@zed-industries/codex-acp", `model="gpt-5-codex"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in args, got %q", want, joined)
		}
	}
}

func TestSpawnHonoursOverrides(t *testing.T) {
	t.Setenv(envBinaryOverride, "/usr/local/bin/codex-acp")
	t.Setenv(envPackageOverride, "ignored")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := (Adapter{}).Spawn(adapter.SpawnOptions{Ctx: ctx})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := cmd.Args[0]; got != "/usr/local/bin/codex-acp" {
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

func TestTomlStringEscapesQuotes(t *testing.T) {
	t.Parallel()
	if got := tomlString(`say "hi"`); !strings.Contains(got, `\"hi\"`) {
		t.Fatalf("tomlString = %q", got)
	}
}
