package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newStatusCmd returns a placeholder `rex status` command that prints
// "not yet implemented". The real implementation lands in a follow-up
// commit; the parent tree wires it now so callers see a stable
// command surface.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current workspace's status",
		Long: `Reports state, draft count per remote, current run if any, hook count,
last-sync time, attached repos, and attached remotes (cli.STATUS.1).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "rex status: not yet implemented")
			return nil
		},
	}
}
