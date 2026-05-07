package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
)

// runSummary is one row reported by `rex run list`.
type runSummary struct {
	RunID      string           `json:"run_id"`
	Status     runner.RunStatus `json:"status"`
	StartedAt  time.Time        `json:"started_at"`
	EndedAt    time.Time        `json:"ended_at,omitempty"`
	NodeEvents int              `json:"node_events"`
	SpecRefs   []string         `json:"spec_refs,omitempty"`
	FromTask   string           `json:"from_task,omitempty"`
}

func newRunListCmd() *cobra.Command {
	var (
		statusFilter   string
		specRefFilters []string
		fromTaskFilter string
		limit          int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs from the workspace event log",
		Long: `Reads the workspace event log, folds run lifecycle events, and prints
recent runs with their effective status.

Filters narrow the list to runs whose RunStartedEvent carries the
matching provenance (execution.RUN.1.2): --spec-ref filters by ACID
in run.started.spec_refs, --from-task filters by the
<spec-id>.<task-id> the run was launched from.`,
		Example: `  rex run list
  rex run --workspace /path/to/ws list --status completed --limit 10
  rex run list --spec-ref sync.ORDER.3
  rex run list --from-task spec-format.define-run-recipes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			summaries, err := readRunSummaries(root)
			if err != nil {
				return err
			}
			if statusFilter != "" {
				filtered := summaries[:0]
				for _, s := range summaries {
					if string(s.Status) == statusFilter {
						filtered = append(filtered, s)
					}
				}
				summaries = filtered
			}
			if len(specRefFilters) > 0 {
				wanted := dedupeRefs(specRefFilters)
				filtered := summaries[:0]
				for _, s := range summaries {
					if matchesAnyRef(s.SpecRefs, wanted) {
						filtered = append(filtered, s)
					}
				}
				summaries = filtered
			}
			if fromTaskFilter != "" {
				filtered := summaries[:0]
				for _, s := range summaries {
					if s.FromTask == fromTaskFilter {
						filtered = append(filtered, s)
					}
				}
				summaries = filtered
			}
			sort.Slice(summaries, func(i, j int) bool {
				return summaries[i].StartedAt.After(summaries[j].StartedAt)
			})
			if limit > 0 && len(summaries) > limit {
				summaries = summaries[:limit]
			}

			if jsonOutput(cmd) {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, s := range summaries {
					if err := enc.Encode(s); err != nil {
						return err
					}
				}
				return nil
			}
			if len(summaries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no runs yet")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tRUN_ID\tSTATUS\tSTARTED\tDURATION\tNODES")
			for _, s := range summaries {
				dur := "—"
				if !s.EndedAt.IsZero() {
					dur = s.EndedAt.Sub(s.StartedAt).Truncate(time.Millisecond).String()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
					runner.FriendlyName(s.RunID),
					s.RunID, s.Status, s.StartedAt.UTC().Format(time.RFC3339), dur, s.NodeEvents)
			}
			return tw.Flush()
		},
	}
	setRelated(cmd,
		"rex run start --shell \"echo hello\"",
		"rex run attach <run-id>",
		"rex run show <run-id>",
	)
	cmd.Flags().StringVar(&statusFilter, "status", "", "only show runs with the given final status")
	cmd.Flags().StringSliceVar(&specRefFilters, "spec-ref", nil, "filter to runs that record this fully-qualified ACID; may be repeated (execution.RUN.1.2)")
	cmd.Flags().StringVar(&fromTaskFilter, "from-task", "", "filter to runs launched from this <spec-id>.<task-id> (execution.RUN.1.2)")
	cmd.Flags().IntVar(&limit, "limit", 0, "show only the N most recent runs (0 = no limit)")
	return cmd
}

// matchesAnyRef returns true when at least one of `wanted` appears in
// `have`. Used by `rex run list --spec-ref` to filter runs whose
// RunStartedEvent referenced any of the requested ACIDs.
func matchesAnyRef(have, wanted []string) bool {
	if len(have) == 0 {
		return false
	}
	for _, w := range wanted {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

// readRunSummaries scans the workspace's events.log and aggregates one
// runSummary per distinct run_id. Runs with no terminal event surface
// as RunStatusRunning; that lets list correctly distinguish in-flight
// runs from completed ones once a daemon model exists.
func readRunSummaries(workspaceRoot string) ([]runSummary, error) {
	path := eventLogPath(workspaceRoot)
	r, err := openReaderForPath(path)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	defer r.Close()

	reg := runDecoderRegistry()
	byID := map[string]*runner.RunSummary{}
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		decoded, err := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if errors.Is(err, event.ErrSkipUnknownType) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var probe runner.RunSummary
		if !probe.FoldEvent(decoded) {
			continue
		}
		s, ok := byID[probe.RunID]
		if !ok {
			s = &runner.RunSummary{}
			byID[probe.RunID] = s
		}
		s.FoldEvent(decoded)
	}

	out := make([]runSummary, 0, len(byID))
	for _, s := range byID {
		out = append(out, runSummary{
			RunID:      s.RunID,
			Status:     s.EffectiveStatus(),
			StartedAt:  s.StartedAt,
			EndedAt:    s.EndedAt,
			NodeEvents: s.NodeEvents,
			SpecRefs:   append([]string(nil), s.SpecRefs...),
			FromTask:   s.FromTask,
		})
	}
	return out, nil
}
