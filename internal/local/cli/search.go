package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/search"
)

// newSearchCmd returns the top-level `rex search` command per
// cli.SHAPE.2 (top-level shortcut for high-frequency operations).
func newSearchCmd() *cobra.Command {
	var (
		workspaceFlag string
		limit         int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search the workspace's index",
		Long: `Runs an FTS5 query against .rex/index.sqlite (covering specs and
audit-log events). Workspace scope only in v1; --all-local /
--remote land alongside the global registry and remote-search work.

Tokens with hyphens, colons, or punctuation are auto-quoted so
identifier-shaped queries (kebab-case names, "type:event") behave
intuitively. AND / OR / NOT pass through unquoted.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootForOrError(workspaceFlag)
			if err != nil {
				return err
			}
			idx, err := search.Open(root)
			if err != nil {
				return err
			}
			defer idx.Close()

			query := joinArgs(args)
			results, err := idx.Search(query, search.SearchOptions{Limit: limit})
			if err != nil {
				return err
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, r := range results {
					if err := enc.Encode(r); err != nil {
						return err
					}
				}
				return nil
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no matches")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TYPE\tID\tTITLE\tSCORE\tSNIPPET")
			for _, r := range results {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%s\n",
					r.EntityType, r.EntityID, r.Title, r.Score, r.Snippet)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().IntVar(&limit, "limit", 25, "max results to return")
	return cmd
}

// joinArgs concatenates positional arguments with single spaces so
// `rex search hello world` and `rex search "hello world"` behave the
// same way at the CLI boundary.
func joinArgs(args []string) string {
	out := ""
	for i, s := range args {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
