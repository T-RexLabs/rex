package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/search"
)

// newSearchCmd returns the top-level `rex search` command per
// cli.SHAPE.2 (top-level shortcut for high-frequency operations).
func newSearchCmd() *cobra.Command {
	var (
		limit int
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
		Example: `  rex search workspace.created
  rex search "type:event"
  rex search sync.ORDER.3 --workspace /path/to/ws`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
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

			if jsonOutput(cmd) {
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
			out := cmd.OutOrStdout()
			color := useColor(cmd, out)
			tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TYPE\tID\tTITLE\tSCORE\tSNIPPET")
			for _, r := range results {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%s\n",
					r.EntityType, r.EntityID, r.Title, r.Score,
					renderSnippet(r.Snippet, color))
			}
			return tw.Flush()
		},
	}
	setRelated(cmd,
		"rex status",
		"rex log tail",
		"rex spec list",
	)
	cmd.Flags().String(workspaceFlagName, "", "workspace root (default: walk up from cwd)")
	cmd.Flags().IntVar(&limit, "limit", 25, "max results to return")
	return cmd
}

// renderSnippet converts FTS5's `<<term>>` highlight markers into
// ANSI bold on a TTY, or strips them otherwise. The raw `<<...>>`
// form is fine for the JSON path (machine consumers want it
// stable); the human path benefits from highlights matching what
// every other CLI grep-like tool does.
func renderSnippet(s string, color bool) string {
	if color {
		s = strings.ReplaceAll(s, "<<", "\x1b[1m")
		s = strings.ReplaceAll(s, ">>", "\x1b[0m")
		return s
	}
	s = strings.ReplaceAll(s, "<<", "")
	s = strings.ReplaceAll(s, ">>", "")
	return s
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
