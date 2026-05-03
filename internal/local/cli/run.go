package cli

import "github.com/spf13/cobra"

// newRunCmd returns the empty `rex run` parent.
func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start, watch, and manage runs",
		Long: `A run is one execution of a workflow DAG against a harness. See
specs/execution.yaml for the run lifecycle.`,
	}
	return cmd
}
