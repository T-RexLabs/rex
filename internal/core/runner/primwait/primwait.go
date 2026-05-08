// Package primwait implements the wait primitive defined in
// execution.PRIM.7: pause the run for a duration. The "or external
// signal" half of PRIM.7 (rex run signal) is reserved for a future
// PR — v1 ships duration-only, which is the more common pattern
// for "give the harness 60s before retrying" recipes.
package primwait

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/rex/internal/core/runner"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "wait"

// DefaultMaxDuration caps a single wait. v1 has no daemon, so a
// run holding for hours would tie up the parent CLI; 24h is well
// above any sensible inline-wait use case but well under "leave
// the laptop for a year".
const DefaultMaxDuration = 24 * time.Hour

// Config is the JSON shape stored in Node.Config.
type Config struct {
	// Duration is how long to wait. Required for v1 (the
	// signal half of PRIM.7 isn't shipped).
	Duration time.Duration `json:"duration"`
}

// Output is what the primitive returns.
type Output struct {
	WaitedNs int64 `json:"waited_ns"`
}

// New returns a Primitive. The optional now func is for tests that
// drive deterministic clocks; nil falls back to time.Now.
type Options struct {
	// Now is injectable so tests can verify duration math without
	// real-time sleeps. When nil, time.Now is used and the wait
	// truly sleeps; tests typically pass tiny durations and let
	// the real sleep run.
	Now func() time.Time
}

// New returns the primitive bound to opts.
func New(opts Options) runner.Primitive {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		var cfg Config
		if len(in.Node.Config) > 0 {
			if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
				return runner.PrimitiveOutput{}, fmt.Errorf("primwait: decode config: %w", err)
			}
		}
		if cfg.Duration <= 0 {
			return runner.PrimitiveOutput{}, errors.New("primwait: duration must be > 0")
		}
		if cfg.Duration > DefaultMaxDuration {
			return runner.PrimitiveOutput{}, fmt.Errorf("primwait: duration %s exceeds max %s", cfg.Duration, DefaultMaxDuration)
		}

		startedAt := now()
		select {
		case <-time.After(cfg.Duration):
		case <-ctx.Done():
			return runner.PrimitiveOutput{}, ctx.Err()
		}
		body, err := json.Marshal(Output{WaitedNs: int64(now().Sub(startedAt))})
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primwait: marshal output: %w", err)
		}
		return runner.PrimitiveOutput{Output: body}, nil
	})
}
