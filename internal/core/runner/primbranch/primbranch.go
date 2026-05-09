// Package primbranch implements the branch primitive defined in
// execution.PRIM.5: "picks one of N edges based on a predicate
// over previous node outputs". The primitive itself is a no-op
// pass-through — the actual branching logic lives in
// runner.Predicate evaluation on each outgoing Edge, performed by
// the executor at edge-scheduling time. A "branch" node is just a
// well-named anchor for that machinery: typing
// `Type: "branch"` in a DAG signals intent so readers know
// predicates on the outgoing edges are load-bearing.
//
// Config is intentionally tiny in v1: an optional `Output`
// field that the primitive emits verbatim so predicates on the
// outgoing edges have a deterministic shape to evaluate against.
// When Output is empty the primitive emits an empty JSON object;
// the upstream node's output is already on RunState for predicate
// evaluation, so the branch node doesn't need to forward it.
package primbranch

import (
	"context"
	"encoding/json"

	"github.com/asabla/rex/internal/core/runner"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "branch"

// Config is the JSON shape stored in Node.Config.
type Config struct {
	// Output is the JSON object the primitive emits unchanged.
	// Predicates on outgoing edges evaluate against this. When
	// nil/empty, the primitive emits `{}`.
	Output json.RawMessage `json:"output,omitempty"`
}

// New returns the branch primitive. It has no configuration
// dependencies; nothing to inject.
func New() runner.Primitive {
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		var cfg Config
		if len(in.Node.Config) > 0 {
			if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
				return runner.PrimitiveOutput{}, err
			}
		}
		out := cfg.Output
		if len(out) == 0 {
			out = json.RawMessage("{}")
		}
		return runner.PrimitiveOutput{Output: out}, nil
	})
}
