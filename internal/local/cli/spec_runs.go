package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/specfmt"
)

// newSpecRunsCmd implements `rex spec runs <id>` per Phase C of
// the spec-schema push: convenience wrapper that lists runs
// whose RunStartedEvent recorded the named spec id (via any
// spec_refs ACID prefixed by `<id>.` or via `from_task` starting
// with the same prefix). With --task, narrows further to runs
// launched from <id>.<task>.
//
// This is the inverse of execution.RUN.1.2: that requirement
// makes runs cite specs; this command makes specs cite back.
func newSpecRunsCmd() *cobra.Command {
	var (
		taskFilter   string
		statusFilter string
		limit        int
	)
	cmd := &cobra.Command{
		Use:   "runs <id>",
		Short: "List runs that cite this spec (or one of its tasks)",
		Long: `Walks the workspace event log and surfaces every run whose
RunStartedEvent recorded the named spec id, either through
` + "`spec_refs`" + ` (any ACID prefixed by ` + "`<id>.`" + `) or through
` + "`from_task`" + ` (a run launched via ` + "`rex run start --from-task`" + `).

With --task, narrows the listing to runs launched from
` + "`<id>.<task>`" + ` specifically — useful for "what runs implement
task X?" inspection.`,
		Example: `  rex spec runs execution
  rex spec runs execution --task dag-primitives
  rex spec runs sync --status completed --limit 5`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSpecIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specID := args[0]
			if !specfmt.IsKebab(specID) {
				return fmt.Errorf("spec id %q is not kebab-case", specID)
			}

			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			summaries, err := readRunSummaries(root)
			if err != nil {
				return err
			}

			fullTask := ""
			if taskFilter != "" {
				if !specfmt.IsKebab(taskFilter) {
					return fmt.Errorf("task id %q is not kebab-case", taskFilter)
				}
				fullTask = specID + "." + taskFilter
			}

			filtered := summaries[:0]
			for _, s := range summaries {
				if !runMatchesSpec(s, specID) {
					continue
				}
				if fullTask != "" && s.FromTask != fullTask {
					continue
				}
				if statusFilter != "" && string(s.Status) != statusFilter {
					continue
				}
				filtered = append(filtered, s)
			}
			sort.Slice(filtered, func(i, j int) bool {
				return filtered[i].StartedAt.After(filtered[j].StartedAt)
			})
			if limit > 0 && len(filtered) > limit {
				filtered = filtered[:limit]
			}

			if jsonOutput(cmd) {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, s := range filtered {
					if err := enc.Encode(s); err != nil {
						return err
					}
				}
				return nil
			}
			if len(filtered) == 0 {
				if fullTask != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "no runs cite %s yet\n", fullTask)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "no runs cite spec %s yet\n", specID)
				}
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tRUN_ID\tSTATUS\tFROM_TASK\tSTARTED")
			for _, s := range filtered {
				from := s.FromTask
				if from == "" {
					from = "—"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					runner.FriendlyName(s.RunID),
					s.RunID, s.Status, from,
					s.StartedAt.UTC().Format(time.RFC3339),
				)
			}
			return tw.Flush()
		},
	}
	setRelated(cmd,
		"rex run list --spec-ref <ACID>",
		"rex run start --from-task <id>.<task>",
		"rex spec show <id>",
	)
	cmd.Flags().StringVar(&taskFilter, "task", "", "narrow to runs launched from <id>.<task>")
	cmd.Flags().StringVar(&statusFilter, "status", "", "only runs with the given final status")
	cmd.Flags().IntVar(&limit, "limit", 0, "show only the N most recent runs (0 = no limit)")
	_ = cmd.RegisterFlagCompletionFunc("task", completeTaskFlagForSpecRunsCmd)
	return cmd
}

// runMatchesSpec reports whether a run summary cites the given
// spec id. Mirrors the web's matchesSpecFilter exactly so the
// CLI and web UI agree on what "runs for this spec" means.
func runMatchesSpec(s runSummary, specID string) bool {
	prefix := specID + "."
	for _, ref := range s.SpecRefs {
		if ref == specID {
			return true
		}
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	if s.FromTask == specID {
		return true
	}
	if strings.HasPrefix(s.FromTask, prefix) {
		return true
	}
	return false
}
