package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// newSyncRebaseCmd implements `rex sync rebase <entity>` (sync.GIT.1,
// sync.GIT.2). Performs a three-way merge of the named git_merged
// entity against the configured remote, writing either the merged
// content or conflict markers + a sidecar.
//
// V1 scope is single-entity: the user passes the `.rex/`-relative
// path of the entity to rebase. Bulk discovery and auto-rebase on
// `rex sync` itself are follow-ups.
func newSyncRebaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebase <entity>",
		Short: "Three-way merge a git_merged entity against the remote",
		Long: `Performs a three-way merge between the local file, the central
node's current revision (fetched via /sync/git GET), and the
last-known-agreed base content cached under .rex/drafts/<remote>.git/.

A clean merge writes the merged content to the local file and refreshes
the base cache. A conflict writes the file with merge markers and a
` + "`<file>.conflict`" + ` sidecar describing each unresolved hunk
(sync.GIT.3); ` + "`rex spec validate`" + ` and ` + "`rex run start`" + ` will refuse to operate
on the entity until ` + "`rex sync resolve`" + ` clears the sidecar (sync.GIT.4).

The entity argument is the path RELATIVE to .rex/ — e.g.
"workspace.yaml", "specs/sync.yaml".`,
		Example: `  rex sync rebase workspace.yaml
  rex sync rebase specs/sync.yaml --remote primary
  rex sync rebase --workspace /path/to/ws specs/execution.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: runSyncRebaseFn,
	}
	setRelated(cmd, "rex sync resolve", "rex sync", "rex pull", "rex push")
	addSyncFlags(cmd)
	return cmd
}

func runSyncRebaseFn(cmd *cobra.Command, args []string) error {
	entity := args[0]
	root, _, url, remote, err := resolveSyncContext(cmd)
	if err != nil {
		return err
	}
	c, err := newAuthedClient(cmd, url)
	if err != nil {
		return err
	}

	res, err := c.RebaseEntity(cmd.Context(), syncclient.RunArgs{
		WorkspaceRoot: root, Remote: remote,
	}, entity)
	if err != nil {
		return err
	}

	wsID, _ := workspaceID(root)
	switch res.Outcome {
	case syncclient.RebaseClean:
		_ = emitAuditEvent(cmd, root, audit.EventTypeSyncGitRebased, audit.SyncGitRebasedEvent{
			WorkspaceID:    wsID,
			Entity:         entity,
			Remote:         remote,
			BaseRevision:   res.BaseRevision,
			LocalRevision:  res.LocalRevision,
			RemoteRevision: res.RemoteRevision,
			MergedRevision: res.LocalRevision, // best-effort; merged file content hash isn't surfaced separately
		})
	case syncclient.RebaseConflict:
		_ = emitAuditEvent(cmd, root, audit.EventTypeSyncGitConflicted, audit.SyncGitConflictedEvent{
			WorkspaceID:    wsID,
			Entity:         entity,
			Remote:         remote,
			BaseRevision:   res.BaseRevision,
			LocalRevision:  res.LocalRevision,
			RemoteRevision: res.RemoteRevision,
			Hunks:          res.Hunks,
		})
	}

	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
			"entity":          entity,
			"outcome":         res.Outcome.String(),
			"local_revision":  res.LocalRevision,
			"remote_revision": res.RemoteRevision,
			"base_revision":   res.BaseRevision,
			"hunks":           res.Hunks,
		})
	}

	out := cmd.OutOrStdout()
	switch res.Outcome {
	case syncclient.RebaseUnchanged:
		fmt.Fprintf(out, "rebase: %s already matches %s (no merge needed)\n", entity, remote)
	case syncclient.RebaseLocalOnly:
		fmt.Fprintf(out, "rebase: %s has no remote revision yet — push it first\n", entity)
	case syncclient.RebaseClean:
		fmt.Fprintf(out, "rebase: %s merged cleanly against %s\n", entity, remote)
	case syncclient.RebaseConflict:
		fmt.Fprintf(out,
			"rebase: %s has %d unresolved hunk(s); edit the file then run `rex sync resolve %s`\n",
			entity, res.Hunks, entity)
	}
	return nil
}
