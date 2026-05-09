package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/storage/synccat"
	"github.com/asabla/rex/internal/core/sync/conflict"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// newSyncResolveCmd implements `rex sync resolve <entity>` per
// sync.GIT.4. Confirms that the user's hand-edited file no longer
// carries any merge markers, drops the `<file>.conflict` sidecar, and
// emits a sync.git.resolved audit event so the resolution propagates.
//
// Refuses to clear the sidecar while merge markers remain, so a
// half-resolved file does not silently re-enter the workspace.
func newSyncResolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve <entity>",
		Short: "Mark a conflicted git_merged entity as resolved",
		Long: `Verifies that the file no longer contains any merge markers,
removes the corresponding ` + "`<file>.conflict`" + ` sidecar, and emits a
` + "`sync.git.resolved`" + ` audit event recording the resolution
(sync.GIT.4).

The entity argument is the path RELATIVE to .rex/ — the same path
` + "`rex sync rebase`" + ` used to flag the conflict.`,
		Example: `  rex sync resolve specs/sync.yaml
  rex sync resolve --workspace /path/to/ws workspace.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: runSyncResolveFn,
	}
	setRelated(cmd, "rex sync rebase", "rex spec validate")
	addSyncFlags(cmd)
	return cmd
}

func runSyncResolveFn(cmd *cobra.Command, args []string) error {
	entity := args[0]
	if cat, ok := synccat.Categorize(entity); !ok || cat != synccat.CategoryGitMerged {
		return fmt.Errorf("%q is not a git_merged entity (sync.CAT.5)", entity)
	}

	root, err := requiredWorkspaceRoot(cmd)
	if err != nil {
		return err
	}
	remote := resolveDefaultRemote(cmd)
	if remote == "" {
		return errors.New("--remote name is required")
	}

	entityPath := filepath.Join(root, ".rex", entity)
	body, err := os.ReadFile(entityPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", entityPath, err)
	}
	if conflict.HasMarkers(body) {
		return fmt.Errorf("%s still contains merge markers; edit them out first", entity)
	}

	sidecarPath := conflict.SidecarPathFor(entityPath)
	side, err := conflict.Read(sidecarPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s has no `.conflict` sidecar — nothing to resolve", entity)
		}
		return fmt.Errorf("read sidecar: %w", err)
	}
	if side.Remote != "" && side.Remote != remote {
		return fmt.Errorf(
			"sidecar was written for remote %q; pass --remote %s to resolve, or rebase against %s first",
			side.Remote, side.Remote, remote)
	}

	if err := conflict.Clear(sidecarPath); err != nil {
		return err
	}

	resolvedRev := proto.GitContentRevision(string(body))
	wsID, _ := workspaceID(root)
	if err := emitAuditEvent(cmd, root, audit.EventTypeSyncGitResolved, audit.SyncGitResolvedEvent{
		WorkspaceID:      wsID,
		Entity:           entity,
		Remote:           remote,
		ResolvedRevision: resolvedRev,
	}); err != nil {
		return err
	}

	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
			"entity":            entity,
			"remote":            remote,
			"resolved_revision": resolvedRev,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"resolved %s; cleared sidecar and emitted sync.git.resolved (revision %s)\n",
		entity, resolvedRev)
	return nil
}
