package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// newSyncCmd, newPushCmd, newPullCmd are top-level shortcuts per
// cli.SHAPE.2. They live in their own file so cli/run.go (rex run)
// stays focused on the runner surface.

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull then push the workspace against its remote",
		Long: `Push local events past the watermark, then pull anything new from
the central node, advancing the per-remote watermark on success.

` + "`--remote <name>`" + ` looks up the URL from
~/.config/rex/remotes.toml (registered via ` + "`rex remote add`" + `).
` + "`--url`" + ` overrides any registry lookup.`,
		RunE: runSyncFn,
	}
	addSyncFlags(cmd)
	return cmd
}

func newPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local drafts to a remote",
		Long: `Reads events past the per-remote watermark from .rex/events.log
and POSTs them to the configured remote. Advances the watermark on
success.`,
		RunE: runPushFn,
	}
	addSyncFlags(cmd)
	return cmd
}

func newPullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull new events from a remote",
		Long: `Streams events past the per-remote watermark from the configured
remote and appends them to .rex/events.log. Advances the watermark
on success.`,
		RunE: runPullFn,
	}
	addSyncFlags(cmd)
	return cmd
}

// addSyncFlags wires the shared flag set onto a sync-shaped command.
// Centralizing here keeps the three commands' surface identical.
func addSyncFlags(cmd *cobra.Command) {
	cmd.Flags().String("workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().String("remote", "primary", "registered remote name (also names the watermark file)")
	cmd.Flags().String("url", "", "central node URL (overrides registry lookup)")
	cmd.Flags().String(remotesPathFlag, "", "override registry path (default: platform user-config dir)")
}

// resolveSyncContext consolidates the workspace/url/remote resolution
// every sync-style command needs. The URL comes from --url first, then
// from the named remote in ~/.config/rex/remotes.toml; an error is
// returned if neither path produces a URL.
func resolveSyncContext(cmd *cobra.Command) (root, logPath, url, remote string, err error) {
	url, _ = cmd.Flags().GetString("url")
	remote, _ = cmd.Flags().GetString("remote")
	if remote == "" {
		return "", "", "", "", errors.New("--remote name is required")
	}
	if url == "" {
		path, perr := registryPath(cmd)
		if perr != nil {
			return "", "", "", "", perr
		}
		reg, lerr := remotes.Load(path)
		if lerr != nil {
			return "", "", "", "", lerr
		}
		r, ok := reg.Get(remote)
		if !ok {
			return "", "", "", "", fmt.Errorf(
				"remote %q not registered; pass --url <url> or run `rex remote add %s <url>`",
				remote, remote)
		}
		url = r.URL
	}
	wsFlag, _ := cmd.Flags().GetString("workspace")
	root, err = workspaceRootFor(wsFlag)
	if err != nil {
		return "", "", "", "", err
	}
	if root == "" {
		return "", "", "", "", errNoWorkspace
	}
	logPath = filepath.Join(root, metaDirName, "events.log")
	return root, logPath, url, remote, nil
}

func runPushFn(cmd *cobra.Command, _ []string) error {
	root, logPath, url, remote, err := resolveSyncContext(cmd)
	if err != nil {
		return err
	}
	c := syncclient.NewClient(url)
	res, err := c.PushOnly(cmd.Context(), syncclient.RunArgs{
		WorkspaceRoot: root, Remote: remote, EventsLogPath: logPath,
	})
	if err != nil {
		return formatSyncError(err)
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"head_id":    res.HeadID,
			"accepted":   res.Accepted,
			"duplicates": res.Duplicates,
		})
	}
	if res.Accepted == 0 && res.Duplicates == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "nothing to push")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"pushed %d event(s) (%d duplicate); head=%s\n",
		res.Accepted, res.Duplicates, res.HeadID)
	return nil
}

func runPullFn(cmd *cobra.Command, _ []string) error {
	root, logPath, url, remote, err := resolveSyncContext(cmd)
	if err != nil {
		return err
	}
	c := syncclient.NewClient(url)
	pulled, err := c.PullOnly(cmd.Context(), syncclient.RunArgs{
		WorkspaceRoot: root, Remote: remote, EventsLogPath: logPath,
	})
	if err != nil {
		return formatSyncError(err)
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"pulled": pulled,
		})
	}
	if pulled == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no new events")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pulled %d event(s)\n", pulled)
	return nil
}

func runSyncFn(cmd *cobra.Command, _ []string) error {
	root, logPath, url, remote, err := resolveSyncContext(cmd)
	if err != nil {
		return err
	}
	c := syncclient.NewClient(url)
	res, err := c.Sync(cmd.Context(), root, remote, logPath)
	if err != nil {
		return formatSyncError(err)
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"pulled":   res.Pulled,
			"head_id":  res.Push.HeadID,
			"pushed":   res.Push.Accepted,
			"duplicates": res.Push.Duplicates,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"sync ok: pulled=%d pushed=%d duplicates=%d head=%s\n",
		res.Pulled, res.Push.Accepted, res.Push.Duplicates, res.Push.HeadID)
	return nil
}

// formatSyncError dresses the typed *ConflictError so the CLI says
// something useful when the rebase engine has not yet landed.
func formatSyncError(err error) error {
	var ce *syncclient.ConflictError
	if errors.As(err, &ce) {
		return fmt.Errorf(
			"diverged from remote (server head=%s; %d events to rebase). Rebase support not yet implemented (sync.GIT.*)",
			ce.ServerHead, len(ce.DivergingTail))
	}
	return err
}
