package adapter

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

type stubAdapter struct {
	name string
	caps Capabilities
}

func (s stubAdapter) Name() string               { return s.name }
func (s stubAdapter) Capabilities() Capabilities { return s.caps }
func (s stubAdapter) Spawn(opts SpawnOptions) (*exec.Cmd, error) {
	return exec.CommandContext(opts.Ctx, "/bin/true"), nil
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if err := r.Register(stubAdapter{name: "alpha"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Lookup("alpha")
	if !ok {
		t.Fatal("Lookup: not found")
	}
	if got.Name() != "alpha" {
		t.Errorf("Name: %q", got.Name())
	}
}

func TestRegistryRejectsDuplicates(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	_ = r.Register(stubAdapter{name: "alpha"})
	if err := r.Register(stubAdapter{name: "alpha"}); err == nil {
		t.Fatal("expected duplicate-registration error")
	} else if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error message: %q", err)
	}
}

func TestRegistryRejectsEmptyName(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if err := r.Register(stubAdapter{name: ""}); err == nil {
		t.Fatal("expected empty-name error")
	}
}

func TestRegistryRejectsNil(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected nil-adapter error")
	}
}

func TestRegistryNamesSorted(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	for _, n := range []string{"gamma", "alpha", "beta"} {
		_ = r.Register(stubAdapter{name: n})
	}
	got := r.Names()
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestRegistryLookupMissing(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("expected not-found")
	}
}

func TestSpawnReturnsCmdBoundToCtx(t *testing.T) {
	t.Parallel()

	a := stubAdapter{name: "alpha"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, err := a.Spawn(SpawnOptions{Ctx: ctx})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if cmd == nil {
		t.Fatal("nil cmd")
	}
}
