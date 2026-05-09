// Package primparallel implements the parallel primitive defined
// in execution.PRIM.6: fan out to N children, fan in once all
// complete, with a configurable failure policy.
//
// The primitive is a "mini-executor". It receives a list of child
// primitive invocations in its Config, dispatches them concurrently
// against the parent registry, then folds the results according to
// FailurePolicy:
//
//   - "any" (default) — any child failure fails the parent
//   - "majority"      — fails when >= ceil(N/2) children fail
//   - "all"           — fails only when every child fails
//
// The Output is a single JSON object listing each child's
// status/output/error verbatim, plus aggregate counts. v1
// intentionally does not emit per-child node lifecycle events:
// adding new event types for partial completion would force a
// state-machine change that the rest of the executor doesn't yet
// understand. Audit observability lives in the parent's
// NodeSucceeded / NodeFailed events, which carry the full child
// detail in the Output payload — readers can reconstruct each
// child's outcome without a new event class.
//
// Cancellation is propagated to every in-flight child via the
// shared context; goroutines bail out on ctx.Err() at their next
// scheduling point. The primitive returns ctx.Err() so the
// executor's checkCancel branch handles it identically to other
// primitives.
package primparallel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/asabla/rex/internal/core/runner"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "parallel"

// FailurePolicy enumerates how the parent's success or failure is
// derived from child outcomes.
type FailurePolicy string

const (
	// PolicyAny — any child failure fails the parent. Default.
	PolicyAny FailurePolicy = "any"
	// PolicyMajority — fails when at least ceil(N/2) children
	// fail. With N=1 this collapses to PolicyAny.
	PolicyMajority FailurePolicy = "majority"
	// PolicyAll — fails only when every child fails.
	PolicyAll FailurePolicy = "all"
)

// Config is the JSON shape stored in Node.Config.
type Config struct {
	// Children describes the fan-out set. Must be non-empty;
	// each child invokes a registered primitive with its own
	// config. Child IDs must be unique within the Config so the
	// Output map can be keyed by them.
	Children []ChildSpec `json:"children"`
	// FailurePolicy selects how the parent's success/failure is
	// derived. Empty string defaults to PolicyAny.
	FailurePolicy FailurePolicy `json:"failure_policy,omitempty"`
}

// ChildSpec is one fan-out target: a primitive type plus its
// config. Children inherit the parent's RunID and execute
// concurrently — they cannot reference each other's outputs (no
// edges within a parallel block).
type ChildSpec struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// ChildResult is one child's outcome captured in the parent
// Output. Status is "succeeded" or "failed"; on success Output
// holds the child's PrimitiveOutput.Output verbatim; on failure
// Error holds the err.Error() string.
type ChildResult struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Output is the parent Node's full result payload.
type Output struct {
	Children       []ChildResult `json:"children"`
	SucceededCount int           `json:"succeeded_count"`
	FailedCount    int           `json:"failed_count"`
	Policy         FailurePolicy `json:"policy"`
}

// Options configure New. Registry is required — without it the
// primitive can't dispatch children.
type Options struct {
	// Registry resolves ChildSpec.Type to a Primitive
	// implementation. Pass the same registry the executor uses;
	// register the parallel primitive *after* its children's
	// primitives so the parent can find them.
	Registry *runner.PrimitiveRegistry
}

// New returns the primitive bound to opts.
func New(opts Options) runner.Primitive {
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		if opts.Registry == nil {
			return runner.PrimitiveOutput{}, errors.New("primparallel: Registry is required")
		}
		var cfg Config
		if len(in.Node.Config) > 0 {
			if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
				return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: decode config: %w", err)
			}
		}
		if len(cfg.Children) == 0 {
			return runner.PrimitiveOutput{}, errors.New("primparallel: children is required (non-empty list)")
		}
		policy := cfg.FailurePolicy
		if policy == "" {
			policy = PolicyAny
		}
		switch policy {
		case PolicyAny, PolicyMajority, PolicyAll:
		default:
			return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: unknown failure_policy %q", policy)
		}

		seenID := make(map[string]struct{}, len(cfg.Children))
		for i, c := range cfg.Children {
			if c.ID == "" {
				return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: child %d has empty id", i)
			}
			if _, dup := seenID[c.ID]; dup {
				return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: duplicate child id %q", c.ID)
			}
			seenID[c.ID] = struct{}{}
			if c.Type == "" {
				return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: child %q has empty type", c.ID)
			}
			if _, ok := opts.Registry.Get(c.Type); !ok {
				return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: child %q references unknown primitive %q", c.ID, c.Type)
			}
		}

		results := make([]ChildResult, len(cfg.Children))
		var wg sync.WaitGroup
		wg.Add(len(cfg.Children))
		for i, child := range cfg.Children {
			i, child := i, child
			go func() {
				defer wg.Done()
				if ctx.Err() != nil {
					results[i] = ChildResult{ID: child.ID, Status: "failed", Error: ctx.Err().Error()}
					return
				}
				prim, _ := opts.Registry.Get(child.Type)
				node := runner.Node{ID: runner.NodeID(child.ID), Type: child.Type, Config: child.Config}
				out, err := prim.Run(ctx, runner.PrimitiveInput{
					RunID: in.RunID,
					Node:  node,
					State: in.State,
				})
				if err != nil {
					results[i] = ChildResult{ID: child.ID, Status: "failed", Error: err.Error(), Output: out.Output}
					return
				}
				results[i] = ChildResult{ID: child.ID, Status: "succeeded", Output: out.Output}
			}()
		}
		wg.Wait()

		// Honor cancellation as a primitive-level error so the
		// executor's checkCancel branch fires after Run returns.
		if ctx.Err() != nil {
			return runner.PrimitiveOutput{}, ctx.Err()
		}

		succeeded, failed := 0, 0
		for _, r := range results {
			switch r.Status {
			case "succeeded":
				succeeded++
			case "failed":
				failed++
			}
		}

		out := Output{
			Children:       results,
			SucceededCount: succeeded,
			FailedCount:    failed,
			Policy:         policy,
		}
		body, err := json.Marshal(out)
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primparallel: marshal output: %w", err)
		}

		// Apply failure policy. Even on failure we keep Output
		// populated so the executor's NodeFailedEvent reader (or
		// the next-attempt retry logic) can see per-child detail.
		if policyFailed(policy, succeeded, failed) {
			return runner.PrimitiveOutput{Output: body},
				fmt.Errorf("primparallel: %d/%d children failed (policy=%s)", failed, len(results), policy)
		}
		return runner.PrimitiveOutput{Output: body}, nil
	})
}

// policyFailed reports whether the child outcome counts trip the
// configured failure policy.
func policyFailed(policy FailurePolicy, succeeded, failed int) bool {
	total := succeeded + failed
	switch policy {
	case PolicyAny:
		return failed > 0
	case PolicyMajority:
		threshold := int(math.Ceil(float64(total) / 2.0))
		return failed >= threshold
	case PolicyAll:
		return failed == total && total > 0
	}
	return failed > 0
}
