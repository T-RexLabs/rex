package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newStatusCmd returns the `rex status` leaf. Reports a compact view
// of the current workspace: identity, state, content counts, and a
// note for the still-deferred remote/run features (cli.STATUS.1).
func newStatusCmd() *cobra.Command {
	var workspaceFlag string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current workspace's status",
		Long: `Reports state, spec/hook/schedule counts, attached repos, and attached
remotes for the workspace at cwd (cli.STATUS.1). Remote/draft/run
fields surface as "—" or "(deferred)" for areas not yet implemented.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root == "" {
				return errNoWorkspace
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runStatus(cmd.OutOrStdout(), root, jsonOut)
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	return cmd
}

func runStatus(out interface{ Write([]byte) (int, error) }, root string, jsonOut bool) error {
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}
	specs, _ := listSpecFiles(specDir(root))
	hooks, _ := countDirEntries(filepath.Join(root, metaDirName, "hooks"))
	schedules, _ := countDirEntries(filepath.Join(root, metaDirName, "schedules"))

	if jsonOut {
		return json.NewEncoder(out).Encode(map[string]any{
			"path":        root,
			"id":          settings.ID,
			"name":        settings.Name,
			"state":       settings.State,
			"specs":       len(specs),
			"hooks":       hooks,
			"schedules":   schedules,
			"remotes":     0,
			"current_run": nil,
			"last_sync":   nil,
		})
	}
	fmt.Fprintf(out, "workspace:   %s (%s) at %s\n", settings.ID, settings.Name, root)
	fmt.Fprintf(out, "state:       %s\n", settings.State)
	fmt.Fprintf(out, "specs:       %d\n", len(specs))
	fmt.Fprintf(out, "hooks:       %d\n", hooks)
	fmt.Fprintf(out, "schedules:   %d\n", schedules)
	fmt.Fprintln(out, "remotes:     none (sync deferred)")
	fmt.Fprintln(out, "current run: none (run-cli deferred)")
	fmt.Fprintln(out, "last sync:   never")
	return nil
}
