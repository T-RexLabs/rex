package cli

import "github.com/spf13/cobra"

// newSpecCmd returns the empty `rex spec` parent.
func newSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Short: "Author, validate, and inspect specs",
		Long: `Specs are the contract for work done in a workspace. Each spec is a
YAML file under .rex/specs/<id>.yaml; the format is described by
specs/spec-format.yaml.`,
	}
	return cmd
}
