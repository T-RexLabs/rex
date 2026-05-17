package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
	"github.com/asabla/rex/internal/local/userconfig"
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
` + "`--url`" + ` overrides any registry lookup.

Subcommands:
  rebase   - three-way merge a single git_merged entity against the remote
  resolve  - confirm a hand-edited conflicted file and clear its sidecar`,
		Example: `  rex sync
  rex sync --workspace /path/to/ws --remote primary
  rex sync --workspace /path/to/ws --url https://central.example.invalid`,
		RunE: runSyncFn,
	}
	setRelated(cmd,
		"rex pull",
		"rex push",
		"rex remote test <name>",
		"rex sync rebase",
		"rex sync resolve",
	)
	addSyncFlags(cmd)
	cmd.AddCommand(newSyncRebaseCmd())
	cmd.AddCommand(newSyncResolveCmd())
	return cmd
}

func newPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local drafts to a remote",
		Long: `Reads events past the per-remote watermark from .rex/events.log
and POSTs them to the configured remote. Advances the watermark on
success.`,
		Example: `  rex push
  rex push --workspace /path/to/ws --remote primary
  rex push --workspace /path/to/ws --url https://central.example.invalid`,
		RunE: runPushFn,
	}
	setRelated(cmd,
		"rex sync",
		"rex pull",
		"rex remote test <name>",
	)
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
		Example: `  rex pull
  rex pull --workspace /path/to/ws --remote primary
  rex pull --workspace /path/to/ws --url https://central.example.invalid`,
		RunE: runPullFn,
	}
	setRelated(cmd,
		"rex sync",
		"rex push",
		"rex remote test <name>",
	)
	addSyncFlags(cmd)
	return cmd
}

// addSyncFlags wires the shared flag set onto a sync-shaped command.
// Centralizing here keeps the three commands' surface identical.
//
// The --remote default is the literal "primary" — this is the
// fallback when neither the user explicitly passes --remote nor
// has set default_remote in ~/.config/rex/config.toml. Default
// resolution at run time consults the user config first
// (storage.GLOBAL.2) so a one-shot `rex push` follows the user's
// config rather than ignoring it.
func addSyncFlags(cmd *cobra.Command) {
	cmd.Flags().String("workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().String("remote", "primary", "registered remote name (also names the watermark file); falls back to ~/.config/rex/config.toml's default_remote when not the literal 'primary'")
	cmd.Flags().String("url", "", "central node URL (overrides registry lookup)")
	cmd.Flags().String(remotesPathFlag, "", "override registry path (default: platform user-config dir)")
	cmd.Flags().String("user-config", "", "override user-config-dir/rex/config.toml path (test-only)")
}

// resolveDefaultRemote consults --remote first; when --remote is
// the literal default ("primary") AND the user-config file sets
// default_remote, that wins. Explicit user override is preserved
// — passing --remote=primary still hits the literal default
// because we can't tell that apart from the unset case.
//
// This is the rare "default in flag is also the sentinel" pattern;
// fine here because v1's bare convention IS "primary" so users
// without a config see no surprise change.
func resolveDefaultRemote(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("remote")
	if v != "" && v != "primary" {
		return v
	}
	cfg, err := loadUserConfig(cmd)
	if err != nil || cfg.DefaultRemote == "" {
		return v
	}
	return cfg.DefaultRemote
}

// loadUserConfig resolves the per-user config path (--user-config
// override or platform default) and loads it. Errors here are
// treated as "no config" by callers — the file is always optional.
func loadUserConfig(cmd *cobra.Command) (*userconfig.Config, error) {
	if v, _ := cmd.Flags().GetString("user-config"); v != "" {
		return userconfig.Load(v)
	}
	return userconfig.LoadDefault()
}

// resolveSyncContext consolidates the workspace/url/remote resolution
// every sync-style command needs. The URL comes from --url first, then
// from the named remote in ~/.config/rex/remotes.toml; an error is
// returned if neither path produces a URL.
func resolveSyncContext(cmd *cobra.Command) (root, logPath, url, remote string, err error) {
	url, _ = cmd.Flags().GetString("url")
	remote = resolveDefaultRemote(cmd)
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
	if wsFlag != "" {
		root, err = strictWorkspaceRoot(cmd)
	} else {
		root, err = requiredWorkspaceRoot(cmd)
	}
	if err != nil {
		return "", "", "", "", err
	}
	logPath = filepath.Join(root, metaDirName, "events.log")
	return root, logPath, url, remote, nil
}

func runPushFn(cmd *cobra.Command, _ []string) error {
	root, logPath, url, remote, err := resolveSyncContext(cmd)
	if err != nil {
		return err
	}
	c, err := newAuthedClient(cmd, url)
	if err != nil {
		return err
	}
	res, err := c.PushOnly(cmd.Context(), syncclient.RunArgs{
		WorkspaceRoot: root, Remote: remote, EventsLogPath: logPath,
	})
	if err != nil {
		return formatSyncError(err)
	}

	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
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
	c, err := newAuthedClient(cmd, url)
	if err != nil {
		return err
	}
	pulled, err := c.PullOnly(cmd.Context(), syncclient.RunArgs{
		WorkspaceRoot: root, Remote: remote, EventsLogPath: logPath,
	})
	if err != nil {
		return formatSyncError(err)
	}

	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
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
	c, err := newAuthedClient(cmd, url)
	if err != nil {
		return err
	}
	res, err := c.Sync(cmd.Context(), root, remote, logPath)
	if err != nil {
		return formatSyncError(err)
	}

	// Push git-merged content (specs / workspace.yaml / amendments
	// / etc) alongside the event push. Best-effort — a git failure
	// surfaces but doesn't roll back the event push; the
	// (workspace_id, path) PK on the central makes re-runs
	// idempotent so the user can fix and re-sync.
	gitReport, gitErr := syncWorkspaceGit(cmd, c, root)

	if jsonOutput(cmd) {
		out := map[string]any{
			"pulled":     res.Pulled,
			"head_id":    res.Push.HeadID,
			"pushed":     res.Push.Accepted,
			"duplicates": res.Push.Duplicates,
		}
		if gitErr != nil {
			out["git_error"] = gitErr.Error()
		}
		out["git_pushed"] = gitReport.Pushed
		out["git_unchanged"] = gitReport.Unchanged
		out["git_conflicted"] = gitReport.Conflicted
		return writeJSON(cmd, out)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"sync ok: pulled=%d pushed=%d duplicates=%d head=%s; git pushed=%d unchanged=%d conflicted=%d\n",
		res.Pulled, res.Push.Accepted, res.Push.Duplicates, res.Push.HeadID,
		len(gitReport.Pushed), len(gitReport.Unchanged), len(gitReport.Conflicted))
	if len(gitReport.Conflicted) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(),
			"  conflicted (resolve locally then re-sync): %s\n",
			strings.Join(gitReport.Conflicted, ", "))
	}
	if gitErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: git sync surfaced an error: %v\n", gitErr)
	}
	return nil
}

// syncWorkspaceGit walks the workspace's git-merged files and
// pushes them via /sync/git. Returns the (possibly partial)
// report alongside any error. The caller decides whether the
// error is fatal — `rex sync` treats it as best-effort and
// just surfaces it.
func syncWorkspaceGit(cmd *cobra.Command, c *syncclient.Client, root string) (syncclient.GitSyncReport, error) {
	entries, err := syncclient.WalkWorkspaceGit(root)
	if err != nil {
		return syncclient.GitSyncReport{}, fmt.Errorf("walk workspace git: %w", err)
	}
	if len(entries) == 0 {
		return syncclient.GitSyncReport{}, nil
	}
	wsID, err := workspaceID(root)
	if err != nil {
		return syncclient.GitSyncReport{}, fmt.Errorf("resolve workspace id: %w", err)
	}
	return c.GitSyncOnly(cmd.Context(), wsID, entries)
}

// formatSyncError dresses the typed *ConflictError so the CLI says
// something useful while the full rebase engine is in flight. The
// rebase-needed flag has already been persisted on the per-remote
// watermark (sync.DRAFT.2), so the user can safely close the terminal
// and pick the rebase up later via `rex status`.
func formatSyncError(err error) error {
	var ce *syncclient.ConflictError
	if errors.As(err, &ce) {
		return fmt.Errorf(
			"diverged from remote (server head=%s; %d events to rebase); flagged the watermark — run `rex pull` to fetch the diverging tail, then `rex push`. Full automatic rebase lands with sync.GIT.*",
			ce.ServerHead, len(ce.DivergingTail))
	}
	return err
}

// newAuthedClient builds a sync client with the local default
// signer attached. The signer is what the client uses to handshake
// with servers that require auth; servers without --keys ignore
// the credential.
func newAuthedClient(cmd *cobra.Command, url string) (*syncclient.Client, error) {
	signer, err := loadOrCreateDefaultSigner(cmd)
	if err != nil {
		return nil, err
	}
	return syncclient.NewClient(url).WithSigner(signer), nil
}
