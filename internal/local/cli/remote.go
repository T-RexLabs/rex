package cli

import (
	"bufio"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// newRemoteCmd returns the `rex remote` parent and wires its leaves
// (cli.REMOTE.1-5).
func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage central nodes the local instance connects to",
		Long: `Each remote is a named central node URL stored in
~/.config/rex/remotes.toml. Other commands look up a remote by name
via --remote <name> instead of typing --url every time.`,
		Example: `  rex remote add primary https://central.example.invalid
  rex remote test primary
  rex remote show primary`,
	}
	setRelated(cmd,
		"rex remote add <name> <url>",
		"rex remote test <name>",
		"rex remote show <name>",
	)
	cmd.AddCommand(newRemoteAddCmd())
	cmd.AddCommand(newRemoteListCmd())
	cmd.AddCommand(newRemoteShowCmd())
	cmd.AddCommand(newRemoteRemoveCmd())
	cmd.AddCommand(newRemoteTestCmd())
	cmd.AddCommand(newRemoteBootstrapCmd())
	cmd.AddCommand(newRemoteLoginCmd())
	return cmd
}

// remotesPathFlag is the dotted path under which the remote subcommands
// honour an override; tests use it to point at a TempDir registry.
const remotesPathFlag = "remotes-file"

func addRemoteSharedFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String(remotesPathFlag, "", "override registry path (default: platform user-config dir)")
	// --workspace doesn't change which registry file gets written
	// (that stays user-level by default per storage.GLOBAL.5), but
	// it tells the audit-emit path which workspace's events.log
	// should record the remote.attached/detached event.
	cmd.PersistentFlags().String(workspaceFlagName, "", "workspace whose audit log should record the change (default: walk up from cwd)")
}

func registryPath(cmd *cobra.Command) (string, error) {
	if v, _ := cmd.Flags().GetString(remotesPathFlag); v != "" {
		return v, nil
	}
	if v, _ := cmd.Root().PersistentFlags().GetString(remotesPathFlag); v != "" {
		return v, nil
	}
	return remotes.DefaultPath()
}

func loadRegistry(cmd *cobra.Command) (*remotes.Registry, string, error) {
	path, err := registryPath(cmd)
	if err != nil {
		return nil, "", err
	}
	reg, err := remotes.Load(path)
	if err != nil {
		return nil, "", err
	}
	return reg, path, nil
}

func newRemoteAddCmd() *cobra.Command {
	var (
		autoYes       bool
		skipHandshake bool
	)
	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Register a named remote (handshake + TOFU confirmation by default)",
		Long: `Adds <name> -> <url> to ~/.config/rex/remotes.toml. By default the
command runs an initial handshake against <url> (` + "`GET /sync/state`" + `) to
fetch the server's public-key fingerprint and protocol version, then
prompts the user to confirm trust before recording the entry
(sync.BOOT.1 + BOOT.1.1: TOFU-with-confirmation).

Flags:
  --yes / -y         accept the observed fingerprint without
                     prompting (for scripts / non-interactive runs).
  --skip-handshake   do not contact the remote at all; record the
                     URL only. The fingerprint stays blank until
                     ` + "`rex remote test`" + ` is run. Useful when the
                     remote isn't reachable yet (e.g. provisioning).

The remote is saved to the registry only after the user accepts the
fingerprint; on rejection or network failure no entry is added.`,
		Example: `  rex remote add primary https://central.example.invalid
  rex remote add primary https://central.example.invalid --yes
  rex remote add primary https://central.example.invalid --skip-handshake`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]
			reg, path, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			entry := remotes.Remote{Name: name, URL: url}

			if !skipHandshake {
				ctx := commandContext(cmd)
				client := syncclient.NewClient(url)
				state, err := client.State(ctx)
				if err != nil {
					return fmt.Errorf("contact %q: %w (pass --skip-handshake to register without contacting the server)", url, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"server fingerprint: %s\nprotocol version:   %d\nactor:              %s\n",
					state.Fingerprint, state.ProtocolVersion, state.Actor,
				)
				if !autoYes {
					ok, err := confirmTrust(cmd, name)
					if err != nil {
						return err
					}
					if !ok {
						fmt.Fprintln(cmd.OutOrStdout(), "declined; remote not registered")
						return nil
					}
				}
				entry.Fingerprint = state.Fingerprint
				entry.LastSeen = time.Now().UTC()
			}

			if err := reg.Add(entry); err != nil {
				return err
			}
			if err := remotes.Save(path, reg); err != nil {
				return err
			}
			emitRemoteAuditIfInWorkspace(cmd, audit.EventTypeRemoteAttached, audit.RemoteAttachedEvent{
				Name: name,
				URL:  url,
			})
			fmt.Fprintf(cmd.OutOrStdout(), "added remote %q -> %s\n", name, url)
			return nil
		},
	}
	setRelated(cmd,
		"rex remote test <name>",
		"rex remote show <name>",
		"rex push --remote <name>",
	)
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "accept the observed fingerprint without prompting")
	cmd.Flags().BoolVar(&skipHandshake, "skip-handshake", false, "do not contact the remote during add; record URL only")
	addRemoteSharedFlags(cmd)
	return cmd
}

// confirmTrust prompts the user to approve the fingerprint just
// observed on the handshake (sync.BOOT.1.1 — TOFU-with-
// confirmation). Reads one line from the command's stdin and
// treats "y" or "yes" (case-insensitive) as accept; anything
// else, including EOF, declines.
//
// Returning (false, nil) means "user declined politely" — caller
// surfaces a no-op message and exits 0. A returned error covers
// "couldn't read stdin at all", which is a hard failure.
func confirmTrust(cmd *cobra.Command, name string) (bool, error) {
	fmt.Fprintf(cmd.OutOrStdout(), "Trust this fingerprint and register %q? [y/N]: ", name)
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// EOF on a closed stdin (non-interactive without pre-piped
		// input) — treat as decline and tell the caller how to
		// proceed.
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func newRemoteListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered remotes",
		Long: `Lists the remotes registered in the local remotes registry, including
their last-seen state and recorded fingerprint.`,
		Example: `  rex remote list
  rex remote list --remotes-file ./remotes.toml --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			items := reg.List()
			if jsonOutput(cmd) {
				return writeJSON(cmd, items)
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no remotes registered")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tURL\tFINGERPRINT\tLAST_SEEN")
			for _, r := range items {
				fp := r.Fingerprint
				if fp == "" {
					fp = "—"
				}
				ls := "—"
				if !r.LastSeen.IsZero() {
					ls = r.LastSeen.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, r.URL, fp, ls)
			}
			return tw.Flush()
		},
	}
	addRemoteSharedFlags(cmd)
	return cmd
}

func newRemoteShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one remote's details",
		Long: `Shows the saved URL, fingerprint, and timestamps for one registered
remote.`,
		Example: `  rex remote show primary
  rex remote show primary --remotes-file ./remotes.toml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			r, ok := reg.Get(args[0])
			if !ok {
				return fmt.Errorf("remote %q not registered", args[0])
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, r)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name:        %s\n", r.Name)
			fmt.Fprintf(out, "url:         %s\n", r.URL)
			fmt.Fprintf(out, "fingerprint: %s\n", emptyDash(r.Fingerprint))
			fmt.Fprintf(out, "added_at:    %s\n", emptyDashTime(r.AddedAt))
			fmt.Fprintf(out, "last_seen:   %s\n", emptyDashTime(r.LastSeen))
			return nil
		},
	}
	addRemoteSharedFlags(cmd)
	return cmd
}

func newRemoteRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a remote",
		Long:  `Removes one remote from the local remotes registry.`,
		Example: `  rex remote remove primary
  rex remote remove primary --remotes-file ./remotes.toml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, path, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			if err := reg.Remove(args[0]); err != nil {
				return err
			}
			if err := remotes.Save(path, reg); err != nil {
				return err
			}
			emitRemoteAuditIfInWorkspace(cmd, audit.EventTypeRemoteDetached, audit.RemoteDetachedEvent{
				Name: args[0],
			})
			fmt.Fprintf(cmd.OutOrStdout(), "removed remote %q\n", args[0])
			return nil
		},
	}
	addRemoteSharedFlags(cmd)
	return cmd
}

// emitRemoteAuditIfInWorkspace tries to record a remote.* audit
// event in the current workspace's events.log. Best-effort: when
// the command runs outside any workspace (the registry file is
// user-level so the action is still legitimate), no audit row
// lands and no error is surfaced. The CLI's own success message
// still fires either way.
//
// The payload is required to embed a `WorkspaceID string` field
// that we'll stamp here from the resolved workspace settings.
func emitRemoteAuditIfInWorkspace(cmd *cobra.Command, eventType string, payload any) {
	root, err := currentWorkspaceRoot(cmd)
	if err != nil || root == "" {
		return
	}
	wsID, err := workspaceID(root)
	if err != nil {
		return
	}
	stamped := stampRemoteWorkspaceID(payload, wsID)
	if err := emitAuditEvent(cmd, root, eventType, stamped); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: emit %s: %v\n", eventType, err)
	}
}

// stampRemoteWorkspaceID sets the WorkspaceID field on the two
// remote.* payload types. Pure type-switch; centralised here so
// the caller doesn't have to thread settings.
func stampRemoteWorkspaceID(payload any, workspaceID string) any {
	switch p := payload.(type) {
	case audit.RemoteAttachedEvent:
		p.WorkspaceID = workspaceID
		return p
	case audit.RemoteDetachedEvent:
		p.WorkspaceID = workspaceID
		return p
	default:
		return payload
	}
}

func newRemoteTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "Verify a remote is reachable; record fingerprint and last_seen",
		Long: `Issues a GET /sync/state against the registered remote. On success
records the server's fingerprint (TOFU) and last_seen timestamp; on
mismatch with a previously-recorded fingerprint, prints a warning and
does not overwrite — the user must remove and re-add the remote.`,
		Example: `  rex remote test primary
  rex remote test primary --remotes-file ./remotes.toml
  rex remote test primary --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, path, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			r, ok := reg.Get(args[0])
			if !ok {
				return fmt.Errorf("remote %q not registered", args[0])
			}

			ctx := commandContext(cmd)
			client := syncclient.NewClient(r.URL)
			state, err := client.State(ctx)
			if err != nil {
				return fmt.Errorf("test %q: %w", r.Name, err)
			}
			if r.Fingerprint != "" && r.Fingerprint != state.Fingerprint {
				return fmt.Errorf(
					"fingerprint mismatch for %q: registered=%s observed=%s. Remove and re-add the remote if this is expected (TOFU per sync.BOOT.1.1)",
					r.Name, r.Fingerprint, state.Fingerprint)
			}
			r.Fingerprint = state.Fingerprint
			r.LastSeen = time.Now().UTC()
			if err := reg.Set(r); err != nil {
				return err
			}
			if err := remotes.Save(path, reg); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name":             r.Name,
					"url":              r.URL,
					"fingerprint":      state.Fingerprint,
					"actor":            state.Actor,
					"protocol_version": state.ProtocolVersion,
					"head_id":          state.HeadID,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s OK — actor=%s head=%s protocol=%d\n",
				r.Name, state.Actor, emptyDash(state.HeadID), state.ProtocolVersion)
			return nil
		},
	}
	setRelated(cmd,
		"rex remote show <name>",
		"rex push --remote <name>",
		"rex pull --remote <name>",
	)
	addRemoteSharedFlags(cmd)
	return cmd
}

// newRemoteBootstrapCmd implements `rex remote bootstrap` — the
// one-shot client side of central-node.BOOT.2. It registers the
// remote (if not already registered), runs the standard
// auth-verify handshake (which auto-joins the caller into the
// remote's default org), and POSTs the one-time admin claim
// token. On success the redeemer's default-org membership is
// upgraded to admin.
//
// Usage:
//
//	rex remote bootstrap <name> <url> --token <T>
//
// The token is the value the central node logs at WARN on
// startup ("admin bootstrap: claim this central node …") and
// also writes to /var/lib/rex/bootstrap.token (or the operator-
// configured path) until it has been redeemed.
func newRemoteBootstrapCmd() *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "bootstrap <name> <url>",
		Short: "Register a remote and redeem its one-time admin token",
		Long: `Registers <name> -> <url>, runs the auth handshake against the
remote, and redeems the one-time admin claim token. After this
succeeds the local node's identity is the admin of the remote's
default org. Pair the token printed in the central node's
startup logs with --token here; both sides should never need to
do this again for that central node.`,
		Example: `  rex remote bootstrap primary https://central.example.invalid --token "$TOKEN"
  rex remote bootstrap primary https://central.example.invalid --token "$TOKEN" --json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]
			reg, path, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			if _, exists := reg.Get(name); !exists {
				if err := reg.Add(remotes.Remote{Name: name, URL: url}); err != nil {
					return err
				}
				if err := remotes.Save(path, reg); err != nil {
					return err
				}
			}

			ctx := commandContext(cmd)
			signer, err := loadOrCreateDefaultSigner(cmd)
			if err != nil {
				return fmt.Errorf("bootstrap %q: %w", name, err)
			}
			client := syncclient.NewClient(url).WithSigner(signer)
			resp, err := client.Bootstrap(ctx, token)
			if err != nil {
				return fmt.Errorf("bootstrap %q: %w", name, err)
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, resp)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s OK — admin granted in org %q (%s); redeemer fingerprint=%s\n",
				name, resp.OrgName, emptyDash(resp.OrgID), resp.Fingerprint,
			)
			return nil
		},
	}
	setRelated(cmd,
		"rex remote show <name>",
		"rex remote test <name>",
		"rex identity show --pub",
	)
	cmd.Flags().StringVar(&token, "token", "", "the one-time admin claim token from the central node")
	if err := cmd.MarkFlagRequired("token"); err != nil {
		_ = err
	}
	addRemoteSharedFlags(cmd)
	return cmd
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func emptyDashTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}
