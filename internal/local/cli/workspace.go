package cli

import "github.com/spf13/cobra"

// newWorkspaceCmd returns the empty `rex workspace` parent. Leaves
// (init/list/show/...) are added by sibling files via init() helpers
// so each leaf's source lives next to its tests without bloating
// root.go.
func newWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage Rex workspaces",
		Long: `A workspace is a container of intent — repositories, specs, scheduled
work, hooks, and connected tools that share one identity and one
event log. See specs/workspace.yaml for the data model.`,
	}
	return cmd
}
