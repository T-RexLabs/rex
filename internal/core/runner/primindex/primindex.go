// Package primindex implements the indexing primitive defined in
// execution.PRIM.8: refresh the workspace's search index after
// content changes. v1 implementation calls search.Index.Rebuild on
// the workspace's .rex/index.sqlite — a full rebuild — because the
// incremental indexer pipeline (search.PIPE.*) is partial.
//
// The primitive is intentionally cheap to compose into a DAG: a
// run that writes specs/runs can drop an `indexing` node at the
// end and trust the index to catch up.
package primindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/search"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "indexing"

// Config is the JSON shape stored in Node.Config. v1 ships no
// knobs — the primitive always does a full rebuild against the
// configured workspace root. Future fields (incremental, files-only)
// land additively per overview.SYS.4.
type Config struct{}

// Output records what the rebuild covered. Same shape as
// search.RebuildStats so downstream readers can correlate.
type Output struct {
	Events     int           `json:"events"`
	Specs      int           `json:"specs"`
	DurationNs time.Duration `json:"duration_ns"`
}

// Options bind the primitive to one workspace's index. Indexer is
// optional — callers can hand in a pre-opened *search.Index for
// determinism in tests; production code passes nil and lets the
// primitive open it itself.
type Options struct {
	WorkspaceRoot string
	Indexer       *search.Index // may be nil; opened on first call
}

// New returns a Primitive bound to opts. WorkspaceRoot is required;
// without it the rebuild can't find .rex/.
func New(opts Options) runner.Primitive {
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		if opts.WorkspaceRoot == "" {
			return runner.PrimitiveOutput{}, errors.New("primindex: WorkspaceRoot is required")
		}
		idx := opts.Indexer
		if idx == nil {
			opened, err := search.Open(opts.WorkspaceRoot)
			if err != nil {
				return runner.PrimitiveOutput{}, fmt.Errorf("primindex: open: %w", err)
			}
			defer opened.Close()
			idx = opened
		}

		// Decode config — currently unused but parsed so a typo'd
		// future-spec config doesn't silently no-op.
		if len(in.Node.Config) > 0 {
			var cfg Config
			if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
				return runner.PrimitiveOutput{}, fmt.Errorf("primindex: decode config: %w", err)
			}
		}

		startedAt := time.Now()
		stats, err := idx.Rebuild(opts.WorkspaceRoot)
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primindex: rebuild: %w", err)
		}
		// Honour ctx cancellation that may have happened during
		// the rebuild. Rebuild itself is synchronous and not ctx-
		// aware in v1; the post-rebuild check is best-effort.
		if err := ctx.Err(); err != nil {
			return runner.PrimitiveOutput{}, err
		}
		body, err := json.Marshal(Output{
			Events:     stats.Events,
			Specs:      stats.Specs,
			DurationNs: time.Since(startedAt),
		})
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primindex: marshal output: %w", err)
		}
		return runner.PrimitiveOutput{Output: body}, nil
	})
}
