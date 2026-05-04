// Package adapter implements the per-harness adapter contract from
// execution.ADAPT.* — a small, registry-keyed seam between the
// generic harness_invocation primitive and the quirks of an
// individual harness (process invocation, env vars, model names,
// capability flags).
//
// An adapter is intentionally a tiny surface: it knows how to build
// the *exec.Cmd that runs the harness as an ACP server on stdio and
// reports which models/modes it exposes. Everything downstream of
// that — session/new, transcript capture, permission routing — is
// shared across harnesses and stays in primharness.
//
// Adapters live alongside this package as `adapter/<harness-name>/`
// subpackages (e.g. adapter/claudecode/). Adding a new harness is
// one new file plus one Register call (ADAPT.3).
package adapter

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"sync"
)

// SpawnOptions are everything an adapter needs to build the *exec.Cmd
// for one invocation. All fields are optional except Ctx; adapters
// fill in their own defaults for anything missing.
type SpawnOptions struct {
	// Ctx bounds the harness lifetime; adapters return a Cmd built
	// with exec.CommandContext(Ctx, ...) so SIGKILL on cancel is
	// handled by os/exec rather than each adapter rolling its own.
	Ctx context.Context

	// Dir is the harness's working directory. Empty means inherit
	// from the parent process (the default for daily local use).
	Dir string

	// Env adds (or overrides) environment entries. Adapters merge
	// these on top of os.Environ() plus their own required vars.
	Env map[string]string

	// Model and Mode pass through from primharness.Config; adapters
	// translate to whatever flag/env shape the harness expects.
	// Empty means "harness default."
	Model string
	Mode  string

	// Extra is a free-form bag for adapter-specific knobs that
	// don't fit anywhere else (e.g. "claude-code-cwd-override").
	// Keep keys lowercase + dotted to avoid collisions across
	// adapters.
	Extra map[string]string
}

// Capabilities is what an adapter advertises to the runner so callers
// (CLI, web UI) can validate Model/Mode before spawning. Empty slices
// mean "any value accepted" — the harness will reject unknown values
// at session/new time.
type Capabilities struct {
	Models      []string
	Modes       []string
	SupportsMCP bool
}

// Adapter is the per-harness interface. Implementations are tiny —
// ADAPT.4 caps them at ~200 LOC — and live in adapter/<name>/.
type Adapter interface {
	// Name is the registry key; the same string CLI users pass via
	// `rex run start --harness <name>`. Convention: kebab-case
	// matching the public product name (e.g. "claude-code",
	// "codex", "opencode").
	Name() string

	// Spawn builds the *exec.Cmd that runs the harness as an ACP
	// server on stdio. The caller takes ownership of the Cmd —
	// pipes, Start, Wait. Adapters do NOT call Start themselves
	// because the runner needs the pipes wired up first.
	Spawn(opts SpawnOptions) (*exec.Cmd, error)

	// Capabilities reports the harness's surface so callers can
	// validate Model/Mode before spawning.
	Capabilities() Capabilities
}

// Registry is a thread-safe map from harness name to Adapter. The
// process default is exposed via Default(); tests build their own
// to avoid sharing state with real adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register installs a in the registry. Returns an error if an
// adapter with the same Name() is already registered — adapters
// must be deterministic; silent overrides hide bugs.
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return fmt.Errorf("adapter: nil adapter")
	}
	name := a.Name()
	if name == "" {
		return fmt.Errorf("adapter: empty Name()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.adapters[name]; ok {
		return fmt.Errorf("adapter: %q already registered", name)
	}
	r.adapters[name] = a
	return nil
}

// MustRegister is the panicking variant — used by adapter packages
// in their init() blocks where a duplicate registration is a
// programming error, not runtime input.
func (r *Registry) MustRegister(a Adapter) {
	if err := r.Register(a); err != nil {
		panic(err)
	}
}

// Lookup returns the adapter registered under name. The bool is
// false when no adapter is registered.
func (r *Registry) Lookup(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// Names returns the registered adapter names sorted alphabetically.
// Used by `rex run start --harness ?` to list options and by tests.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for n := range r.adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// defaultRegistry is the process-wide singleton. Adapter packages
// register into it from their init() blocks.
var defaultRegistry = NewRegistry()

// Default returns the process-wide registry. CLI and web UI code
// looks up adapters here; tests should use NewRegistry() to stay
// isolated.
func Default() *Registry { return defaultRegistry }
