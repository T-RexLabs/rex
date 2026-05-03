// Package primspec implements the spec_validate primitive defined in
// execution.PRIM.3.
//
// In v1 the primitive is a stub: it accepts the same Config shape the
// final implementation will use and returns an "unimplemented" Output
// with a non-zero ExitCode equivalent. The stub exists so DAGs that
// reference spec_validate can be authored, validated, and replayed
// before the spec validator (build-order step 3) lands. Replacing
// the implementation will not require Config or Output changes.
package primspec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/asabla/rex/internal/core/runner"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "spec_validate"

// ErrNotImplemented is returned by Run until the validator lands.
// Callers can branch on this with errors.Is to decide whether the
// failure is "real" or expected during early bring-up.
var ErrNotImplemented = errors.New("primspec: spec validator not yet implemented")

// Config is the JSON shape stored in Node.Config. The fields are the
// minimum the final implementation will need; additional fields can be
// added later (overview.SYS.4 — schema evolution is additive).
type Config struct {
	// Specs is a list of spec file paths, relative to the workspace
	// root. Empty means "validate every spec in .rex/specs/".
	Specs []string `json:"specs,omitempty"`
	// FailFast stops at the first invalid spec. The full
	// implementation will honour this; the stub does not run any
	// checks.
	FailFast bool `json:"fail_fast,omitempty"`
}

// Output mirrors what the real primitive will produce. Stub runs
// always set Implemented=false and leave Results empty.
type Output struct {
	Implemented bool     `json:"implemented"`
	Results     []Result `json:"results,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// Result is the per-spec validation outcome.
type Result struct {
	Path  string   `json:"path"`
	Valid bool     `json:"valid"`
	Error string   `json:"error,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

// New returns the stub Primitive. The real implementation will replace
// the Run body without changing the constructor signature.
func New() runner.Primitive {
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		var cfg Config
		if len(in.Node.Config) > 0 {
			if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
				return runner.PrimitiveOutput{}, fmt.Errorf("primspec: decode config: %w", err)
			}
		}
		body, err := json.Marshal(Output{
			Implemented: false,
			Note:        ErrNotImplemented.Error(),
		})
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primspec: marshal output: %w", err)
		}
		return runner.PrimitiveOutput{Output: body}, ErrNotImplemented
	})
}
