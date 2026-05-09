// Package cli — `rex workspace clone` (workspace.LIFE.2 / sync.BOOT.2).
package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// newWorkspaceCloneCmd implements `rex workspace clone <remote-url>
// <workspace-id> [path]` per workspace.LIFE.2.
//
// v1 scope:
//   - clones one workspace's event-sourced state from one remote
//     (the git-merged content surface — specs/, schedules/, hooks/
//     — lands when sync.GIT.* ships)
//   - workspace_id is required positional because central nodes are
//     multi-tenant; a single URL doesn't disambiguate
//   - the remote is registered locally under --remote-name (default
//     "primary") and a watermark file at .rex/drafts/<remote>.toml
//     is stamped to the last received event
//
// Reconstructs workspace.yaml from the workspace.created event seen
// during the pull, then folds any subsequent workspace.archived /
// unarchived / deleted events so the cloned state field matches the
// authoritative current state (not just the initial "active").
// Without a workspace.created event the clone fails with a clear
// "workspace.created not found in stream" message.
func newWorkspaceCloneCmd() *cobra.Command {
	var (
		remoteName   string
		identityFlag string
	)
	cmd := &cobra.Command{
		Use:   "clone <remote-url> <workspace-id> [path]",
		Short: "Clone a workspace's event log from a remote",
		Long: `Pulls one workspace's event-sourced history from a remote into a
fresh local workspace. The events drive workspace.yaml
reconstruction (id/name/created_at/state from the workspace.created
event); git-merged content (specs/, schedules/, hooks/, templates/)
follows when sync.GIT.* ships — v1 clone is event-sourced state only.

Steps:
  1. Resolve the target path (default: ./<workspace-id>) and
     refuse to clobber an existing .rex/.
  2. Hand-shake against the remote (challenge/verify; same path
     'rex remote test' uses) and stream all events past the empty
     watermark.
  3. Filter to the requested workspace_id and append matching
     records verbatim into the new workspace's events.log
     (raw append — events keep their original HLC + signature).
  4. Write workspace.yaml from the workspace.created event.
  5. Register the remote in ~/.config/rex/remotes.toml (under
     --remote-name) and the workspace in registry.toml.
  6. Stamp the per-remote watermark.`,
		Example: `  rex workspace clone https://central.example.invalid demo
  rex workspace clone https://central.example.invalid demo ./projects/demo
  rex workspace clone https://central.example.invalid demo --remote-name staging`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			remoteURL := strings.TrimRight(args[0], "/")
			workspaceID := args[1]
			path := workspaceID
			if len(args) == 3 {
				path = args[2]
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve %q: %w", path, err)
			}
			if _, err := os.Stat(filepath.Join(abs, metaDirName)); err == nil {
				return fmt.Errorf("%s already contains a Rex workspace; refusing to overwrite", abs)
			}

			if err := initWorkspaceSkeleton(abs); err != nil {
				return err
			}

			signer, err := loadOrCreateDefaultSigner(cmd)
			if err != nil {
				return err
			}
			_ = identityFlag // honoured by loadOrCreateDefaultSigner via the persistent --identity-dir flag

			ctx := commandContext(cmd)
			c := syncclient.NewClient(remoteURL).WithSigner(signer)

			// Fetch state up-front so a misconfigured URL fails
			// fast (and we can record the server's fingerprint
			// in remotes.toml).
			state, err := c.State(ctx)
			if err != nil {
				return fmt.Errorf("contact remote: %w", err)
			}

			// Pull every event past the empty cursor; filter to
			// workspaceID; raw-append matching records.
			logPath := filepath.Join(abs, metaDirName, "events.log")
			f, err := syncclient.OpenAppend(logPath)
			if err != nil {
				return err
			}
			defer f.Close()

			var (
				lastID         string
				matched        int
				sawCreated     bool
				createdPayload audit.WorkspaceCreatedEvent
				// resolvedState folds the latest state-transition
				// event seen in the stream so the cloned
				// workspace.yaml reflects the current state, not
				// just the initial "active" from
				// workspace.created (workspace.LIFE.3).
				resolvedState = workspaceStateActive
			)
			_, err = c.Pull(ctx, "", func(rec eventlog.Record) error {
				if rec.WorkspaceID != workspaceID {
					return nil
				}
				if err := syncclient.AppendRaw(f, rec); err != nil {
					return err
				}
				matched++
				lastID = rec.ID
				switch rec.Type {
				case audit.EventTypeWorkspaceCreated:
					if err := json.Unmarshal(rec.Payload, &createdPayload); err == nil {
						sawCreated = true
					}
				case audit.EventTypeWorkspaceArchived:
					resolvedState = workspaceStateArchived
				case audit.EventTypeWorkspaceUnarchived:
					resolvedState = workspaceStateActive
				case audit.EventTypeWorkspaceDeleted:
					resolvedState = workspaceStateDeleted
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("pull events: %w", err)
			}
			if matched == 0 {
				return fmt.Errorf("remote has no events for workspace %q", workspaceID)
			}
			if !sawCreated {
				return fmt.Errorf("workspace %q is missing a workspace.created event in the remote stream", workspaceID)
			}

			// Reconstruct workspace.yaml from the workspace.created
			// event payload, then fold subsequent state-transition
			// events to capture the latest state (workspace.LIFE.3).
			// Events are streamed in HLC order so the last
			// transition we observe is the authoritative current
			// state.
			settings := workspaceSettings{
				ID:        createdPayload.WorkspaceID,
				Name:      createdPayload.Name,
				State:     resolvedState,
				CreatedAt: createdPayload.CreatedAt,
			}
			body, err := yaml.Marshal(settings)
			if err != nil {
				return fmt.Errorf("marshal workspace.yaml: %w", err)
			}
			if err := os.WriteFile(filepath.Join(abs, metaDirName, "workspace.yaml"), body, 0o644); err != nil {
				return err
			}

			// Watermark stamps the last seen event id so the next
			// `rex pull` continues past it.
			wm := syncclient.Watermark{
				Remote:           remoteName,
				LastAckedEventID: lastID,
				AckedAt:          time.Now().UTC(),
			}
			if err := syncclient.SaveWatermark(abs, wm); err != nil {
				return fmt.Errorf("save watermark: %w", err)
			}

			// Register the remote locally and the workspace in
			// the global registry. Both best-effort; clone has
			// already produced a usable workspace at this point.
			if err := registerCloneRemote(cmd, remoteName, remoteURL, state.Fingerprint); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: register remote: %v\n", err)
			}
			if err := registerWorkspace(cmd, settings.ID, abs, remoteName); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: register %s: %v\n", settings.ID, err)
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"id":     settings.ID,
					"name":   settings.Name,
					"path":   abs,
					"remote": remoteName,
					"events": matched,
					"head":   lastID,
				})
			}
			printConfirmation(cmd, "Cloned workspace %q at %s (%d event(s) from %s, head %s)\n",
				settings.ID, abs, matched, remoteName, lastID)
			return nil
		},
	}
	cmd.Flags().StringVar(&remoteName, "remote-name", "primary", "local alias for the remote in remotes.toml")
	cmd.Flags().StringVar(&identityFlag, "identity-dir", "", "override identity store path (default: platform user-config dir/rex/identity/)")
	cmd.Flags().String(remotesPathFlag, "", "override registry path (default: platform user-config dir)")
	addRegistryFlag(cmd)
	setRelated(cmd, "rex workspace init", "rex remote add", "rex pull")
	return cmd
}

// initWorkspaceSkeleton mirrors `rex workspace init`'s on-disk
// layout step but stops short of writing workspace.yaml +
// emitting workspace.created. Clone reconstructs both from the
// pulled events.
func initWorkspaceSkeleton(abs string) error {
	rexDir := filepath.Join(abs, metaDirName)
	if err := os.MkdirAll(rexDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", rexDir, err)
	}
	for _, sub := range initSubdirs {
		if err := os.MkdirAll(filepath.Join(rexDir, sub), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", sub, err)
		}
	}
	return nil
}

// registerCloneRemote upserts the remote into the local registry
// (~/.config/rex/remotes.toml or --remotes-file). Records the
// server's fingerprint for the TOFU check `rex remote test` does.
func registerCloneRemote(cmd *cobra.Command, name, remoteURL, fingerprint string) error {
	if !remotes.IsValidName(name) {
		return fmt.Errorf("--remote-name %q must be kebab-case", name)
	}
	if _, err := url.Parse(remoteURL); err != nil {
		return fmt.Errorf("invalid remote url %q: %w", remoteURL, err)
	}
	path, err := registryPath(cmd)
	if err != nil {
		return err
	}
	reg, err := remotes.Load(path)
	if err != nil {
		return err
	}
	if err := reg.Set(remotes.Remote{
		Name:        name,
		URL:         remoteURL,
		Fingerprint: fingerprint,
	}); err != nil {
		return err
	}
	return remotes.Save(path, reg)
}

// initSubdirs is shared with workspace.go's init body. The list
// lives in the workspace.go for now; clone references it via the
// package-level var defined there.
var _ = initSubdirs

// makeRunnerLinkSilent is here to silence the "imported and not
// used" check the runner import would trip if all event-type
// references move into helpers; keep audit and runner referenced
// at top-level to make future maintenance obvious.
var _ = runner.WorkTypeNonSpec
