package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/snapshot"
)

// newSnapshotCmd returns the `rex snapshot` parent and wires its
// leaves (cli.SNAP.1).
func newSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage local snapshots",
		Long: `A snapshot captures the workspace's git-merged content
(workspace.yaml, specs, schedules, templates, hooks) plus a manifest
recording the events.log head at snapshot time. Snapshots live at
.rex/snapshots/<id>/ and are local-only (storage.SNAP.2). V1 ships
manual triggers; auto-triggers (every-N-events, every-T-duration)
land alongside the daemon work.`,
	}
	addWorkspacePersistentFlag(cmd)
	cmd.AddCommand(newSnapshotCreateCmd())
	cmd.AddCommand(newSnapshotListCmd())
	cmd.AddCommand(newSnapshotRestoreCmd())
	cmd.AddCommand(newSnapshotPruneCmd())
	return cmd
}

func newSnapshotCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new snapshot of the workspace's git-merged content",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootForOrError(workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			m, err := snapshot.Create(snapshot.CreateOptions{WorkspaceRoot: root})
			if err != nil {
				return err
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(m)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"snapshot %s created (event head=%s, %d component(s))\n",
				m.SnapshotID, dashIfEmpty(m.LastEventID), len(m.CapturedComponents))
			return nil
		},
	}
	return cmd
}

func newSnapshotListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshots in the workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootForOrError(workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			items, err := snapshot.List(root)
			if err != nil {
				return err
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no snapshots yet")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SNAPSHOT_ID\tCREATED_AT\tEVENT_HEAD\tCOMPONENTS")
			for _, m := range items {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n",
					m.SnapshotID,
					m.CreatedAt.UTC().Format(time.RFC3339),
					dashIfEmpty(m.LastEventID),
					len(m.CapturedComponents))
			}
			return tw.Flush()
		},
	}
	return cmd
}

func newSnapshotRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Roll the workspace's git-merged content back to a snapshot",
		Long: `Replaces workspace.yaml, specs/, schedules/, templates/, and hooks/
with their snapshot-time contents (storage.SNAP.4). events.log and
transcripts/ are left untouched.

Restore is best-effort, not transactional: a crash mid-restore can
leave the workspace partially restored. Take a fresh snapshot
immediately before restoring if you need a manual rollback path.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootForOrError(workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			m, err := snapshot.Restore(root, args[0])
			if err != nil {
				return err
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(m)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"restored snapshot %s (created %s, %d component(s))\n",
				m.SnapshotID, m.CreatedAt.UTC().Format(time.RFC3339), len(m.CapturedComponents))
			return nil
		},
	}
	return cmd
}

func newSnapshotPruneCmd() *cobra.Command {
	var (
		keepLast    int
		keepMonthly bool
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete snapshots according to the retention policy (storage.SNAP.5)",
		Long: `Default policy: keep the 7 most recent snapshots, plus one snapshot per
calendar month forever. Override with --keep-last and --keep-monthly.
--dry-run reports what would be deleted without removing anything.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootForOrError(workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			policy := snapshot.Policy{KeepLast: keepLast, KeepMonthly: keepMonthly}

			if dryRun {
				items, err := snapshot.List(root)
				if err != nil {
					return err
				}
				toDelete := snapshotsToDelete(items, policy)
				return reportPrune(cmd, toDelete, true)
			}

			deleted, err := snapshot.Prune(root, policy)
			if err != nil {
				return err
			}
			return reportPrune(cmd, deleted, false)
		},
	}
	cmd.Flags().IntVar(&keepLast, "keep-last", snapshot.DefaultPolicy.KeepLast, "retain the N most recent snapshots")
	cmd.Flags().BoolVar(&keepMonthly, "keep-monthly", snapshot.DefaultPolicy.KeepMonthly, "retain one snapshot per calendar month")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be deleted without removing anything")
	return cmd
}

// snapshotsToDelete mirrors the library's pickDeletes for the
// --dry-run path. Kept here rather than exported from the library
// because the library's pickDeletes is the only consumer otherwise.
func snapshotsToDelete(items []snapshot.Manifest, policy snapshot.Policy) []string {
	keep := make(map[string]bool, len(items))
	seenMonth := make(map[string]struct{})
	for i, s := range items {
		if policy.KeepLast > 0 && i < policy.KeepLast {
			keep[s.SnapshotID] = true
		}
		if policy.KeepMonthly {
			month := s.CreatedAt.UTC().Format("2006-01")
			if _, dup := seenMonth[month]; !dup {
				seenMonth[month] = struct{}{}
				keep[s.SnapshotID] = true
			}
		}
	}
	out := make([]string, 0, len(items))
	for _, s := range items {
		if !keep[s.SnapshotID] {
			out = append(out, s.SnapshotID)
		}
	}
	return out
}

func reportPrune(cmd *cobra.Command, ids []string, dryRun bool) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"dry_run": dryRun,
			"deleted": ids,
		})
	}
	prefix := "deleted"
	if dryRun {
		prefix = "would delete"
	}
	if len(ids) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: nothing\n", prefix)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %d snapshot(s):\n", prefix, len(ids))
	for _, id := range ids {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", id)
	}
	return nil
}

// workspaceRootForOrError wraps workspaceRootFor and returns
// errNoWorkspace when no workspace is found OR when the explicit
// --workspace path lacks a .rex/ directory.
func workspaceRootForOrError(flag string) (string, error) {
	root, err := workspaceRootFor(flag)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", errNoWorkspace
	}
	// workspaceRootFor trusts --workspace verbatim; for snapshot
	// operations that would silently create .rex/snapshots/ in any
	// directory the user pointed at. Re-check that .rex/ exists.
	if _, err := os.Stat(filepath.Join(root, metaDirName)); err != nil {
		if os.IsNotExist(err) {
			return "", errNoWorkspace
		}
		return "", err
	}
	return root, nil
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
