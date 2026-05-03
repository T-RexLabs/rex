package cli

import "github.com/spf13/cobra"

// newHooksCmd returns the empty `rex hooks` parent.
func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect installed hooks",
		Long: `Hooks are file-based observers that fire on workspace events. Per
specs/hooks.yaml, v1 supports only post-event observers; pre-event
gating is deferred.`,
	}
	return cmd
}
