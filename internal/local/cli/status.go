package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	syncclient "github.com/asabla/rex/internal/local/sync"
)

// newStatusCmd returns the `rex status` leaf. Reports a compact view
// of the current workspace: identity, state, content counts, and
// per-remote draft state (cli.STATUS.1, sync.DRAFT.2).
func newStatusCmd() *cobra.Command {
	var workspaceFlag string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current workspace's status",
		Long: `Reports state, spec/hook/schedule counts, attached repos, and
per-remote draft counts for the workspace at cwd (cli.STATUS.1,
sync.DRAFT.2). Run-status surfaces as "(deferred)" until the daemon
model exists.`,
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

// remoteStatus is one row's worth of per-remote draft state.
type remoteStatus struct {
	Name             string    `json:"name"`
	LastAckedEventID string    `json:"last_acked_event_id"`
	AckedAt          time.Time `json:"acked_at"`
	Drafts           int       `json:"drafts"`
}

func runStatus(out interface{ Write([]byte) (int, error) }, root string, jsonOut bool) error {
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}
	specs, _ := listSpecFiles(specDir(root))
	hooks, _ := countDirEntries(filepath.Join(root, metaDirName, "hooks"))
	schedules, _ := countDirEntries(filepath.Join(root, metaDirName, "schedules"))
	logPath := filepath.Join(root, metaDirName, "events.log")

	watermarks, err := syncclient.ListWatermarks(root)
	if err != nil {
		return err
	}
	remotes := make([]remoteStatus, 0, len(watermarks))
	for _, w := range watermarks {
		count, err := syncclient.CountEventsAfter(logPath, w.LastAckedEventID)
		if err != nil {
			return fmt.Errorf("count drafts for %q: %w", w.Remote, err)
		}
		remotes = append(remotes, remoteStatus{
			Name:             w.Remote,
			LastAckedEventID: w.LastAckedEventID,
			AckedAt:          w.AckedAt,
			Drafts:           count,
		})
	}
	totalDrafts, err := syncclient.CountEventsAfter(logPath, "")
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(out).Encode(map[string]any{
			"path":         root,
			"id":           settings.ID,
			"name":         settings.Name,
			"state":        settings.State,
			"specs":        len(specs),
			"hooks":        hooks,
			"schedules":    schedules,
			"events_total": totalDrafts,
			"remotes":      remotes,
			"current_run":  nil,
		})
	}
	fmt.Fprintf(out, "workspace:   %s (%s) at %s\n", settings.ID, settings.Name, root)
	fmt.Fprintf(out, "state:       %s\n", settings.State)
	fmt.Fprintf(out, "specs:       %d\n", len(specs))
	fmt.Fprintf(out, "hooks:       %d\n", hooks)
	fmt.Fprintf(out, "schedules:   %d\n", schedules)
	fmt.Fprintf(out, "events:      %d total\n", totalDrafts)
	fmt.Fprintln(out, "current run: none (run-cli deferred)")
	if len(remotes) == 0 {
		fmt.Fprintln(out, "remotes:     none (run `rex push --url ...` to attach)")
		return nil
	}
	fmt.Fprintln(out, "remotes:")
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  REMOTE\tDRAFTS\tLAST_ACKED\tLAST_SYNC")
	for _, r := range remotes {
		acked := "—"
		if !r.AckedAt.IsZero() {
			acked = r.AckedAt.UTC().Format(time.RFC3339)
		}
		head := r.LastAckedEventID
		if head == "" {
			head = "—"
		}
		fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n", r.Name, r.Drafts, head, acked)
	}
	return tw.Flush()
}
