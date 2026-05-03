package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

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
	}
	cmd.AddCommand(newRemoteAddCmd())
	cmd.AddCommand(newRemoteListCmd())
	cmd.AddCommand(newRemoteShowCmd())
	cmd.AddCommand(newRemoteRemoveCmd())
	cmd.AddCommand(newRemoteTestCmd())
	return cmd
}

// remotesPathFlag is the dotted path under which the remote subcommands
// honour an override; tests use it to point at a TempDir registry.
const remotesPathFlag = "remotes-file"

func addRemoteSharedFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String(remotesPathFlag, "", "override registry path (default: platform user-config dir)")
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
	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Register a named remote",
		Long: `Adds <name> -> <url> to ~/.config/rex/remotes.toml. The remote is
not contacted; use ` + "`rex remote test`" + ` to verify connectivity and
record the server's fingerprint.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]
			reg, path, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			if err := reg.Add(remotes.Remote{Name: name, URL: url}); err != nil {
				return err
			}
			if err := remotes.Save(path, reg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added remote %q -> %s\n", name, url)
			return nil
		},
	}
	addRemoteSharedFlags(cmd)
	return cmd
}

func newRemoteListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			items := reg.List()
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			r, ok := reg.Get(args[0])
			if !ok {
				return fmt.Errorf("remote %q not registered", args[0])
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
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
		Args:  cobra.ExactArgs(1),
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
			fmt.Fprintf(cmd.OutOrStdout(), "removed remote %q\n", args[0])
			return nil
		},
	}
	addRemoteSharedFlags(cmd)
	return cmd
}

func newRemoteTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "Verify a remote is reachable; record fingerprint and last_seen",
		Long: `Issues a GET /sync/state against the registered remote. On success
records the server's fingerprint (TOFU) and last_seen timestamp; on
mismatch with a previously-recorded fingerprint, prints a warning and
does not overwrite — the user must remove and re-add the remote.`,
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

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
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
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
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
