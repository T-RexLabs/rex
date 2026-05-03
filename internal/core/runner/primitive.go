package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Primitive is one unit of work the executor knows how to run. The
// concrete primitives in v1 are harness_invocation, shell_exec, and
// spec_validate (execution.PRIM.1-3); the others come later.
type Primitive interface {
	Run(ctx context.Context, in PrimitiveInput) (PrimitiveOutput, error)
}

// PrimitiveInput is what the executor hands to Run. It is intentionally
// small: a Node carrying its own JSON config, plus a read-only view of
// RunState so primitives can peek at upstream outputs.
type PrimitiveInput struct {
	RunID string
	Node  Node
	State *RunState
}

// UpstreamOutput returns the structured output of an upstream node. It
// is the canonical way for a primitive to read a predecessor's result
// instead of poking at PrimitiveInput.State directly.
func (in PrimitiveInput) UpstreamOutput(id NodeID) (json.RawMessage, bool) {
	if in.State == nil {
		return nil, false
	}
	ns, ok := in.State.Nodes[id]
	if !ok || ns.Status != NodeStatusSucceeded {
		return nil, false
	}
	return ns.Output, true
}

// PrimitiveOutput carries whatever structured result the primitive
// produced. The bytes land verbatim in NodeSucceededEvent.Output.
type PrimitiveOutput struct {
	Output json.RawMessage
}

// PrimitiveFunc adapts a function to the Primitive interface.
type PrimitiveFunc func(ctx context.Context, in PrimitiveInput) (PrimitiveOutput, error)

// Run satisfies Primitive.
func (f PrimitiveFunc) Run(ctx context.Context, in PrimitiveInput) (PrimitiveOutput, error) {
	return f(ctx, in)
}

// PrimitiveRegistry maps a Node.Type string to a Primitive
// implementation. A Registry is safe for concurrent reads after
// Register calls have completed.
type PrimitiveRegistry struct {
	mu    sync.RWMutex
	prims map[string]Primitive
}

// NewPrimitiveRegistry returns an empty registry.
func NewPrimitiveRegistry() *PrimitiveRegistry {
	return &PrimitiveRegistry{prims: make(map[string]Primitive)}
}

// Register associates name with p. Re-registering the same name
// overwrites the previous entry; the executor expects a single
// registration per name at startup.
func (r *PrimitiveRegistry) Register(name string, p Primitive) {
	if name == "" {
		panic("runner: cannot register primitive with empty name")
	}
	if p == nil {
		panic("runner: cannot register nil primitive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prims[name] = p
}

// Get returns the Primitive for name and whether it was found.
func (r *PrimitiveRegistry) Get(name string) (Primitive, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.prims[name]
	return p, ok
}

// names returns all registered names; used by Executor to validate a
// DAG before starting.
func (r *PrimitiveRegistry) names() map[string]struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]struct{}, len(r.prims))
	for k := range r.prims {
		out[k] = struct{}{}
	}
	return out
}

// ErrPrimitiveUnknown is returned when a Node references a Type the
// registry has no implementation for.
var ErrPrimitiveUnknown = errors.New("runner: primitive type not registered")

// validateRegistered checks every Node in dag has a registered
// primitive. Returns a joined error listing every gap.
func validateRegistered(dag DAG, reg *PrimitiveRegistry) error {
	known := reg.names()
	var gaps []error
	for _, n := range dag.Nodes {
		if _, ok := known[n.Type]; !ok {
			gaps = append(gaps, fmt.Errorf("%w: node %q type %q", ErrPrimitiveUnknown, n.ID, n.Type))
		}
	}
	return errors.Join(gaps...)
}
