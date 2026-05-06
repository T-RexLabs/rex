package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/identity"
)

// newIdentityCmd returns the `rex identity` parent and wires its
// leaves. v1 ships show + list; create/use land alongside the
// challenge-response handshake.
func newIdentityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Manage local identities",
		Long: `An identity is an ed25519 keypair plus a kebab-case handle. Identity
material lives at ~/.config/rex/identity/ (or the platform
equivalent). Use ` + "`rex identity show --pub`" + ` to fetch the public
key in PEM form when registering with a remote.`,
		Example: `  rex identity show
  rex identity show --pub
  rex identity list`,
	}
	setRelated(cmd,
		"rex identity show",
		"rex identity list",
		"rex remote bootstrap <name> <url> --token <token>",
	)
	addIdentitySharedFlags(cmd)
	cmd.AddCommand(newIdentityShowCmd())
	cmd.AddCommand(newIdentityListCmd())
	return cmd
}

const identityDirFlag = "identity-dir"

// addIdentitySharedFlags attaches the --identity-dir override to a
// command. Persistent so leaves inherit it.
func addIdentitySharedFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String(identityDirFlag, "",
		"override identity store path (default: REX_IDENTITY_DIR or platform user-config dir)")
}

// resolveIdentityDir walks the same precedence loadOrCreateDefaultSigner
// uses: --identity-dir flag, REX_IDENTITY_DIR env var, platform default.
func resolveIdentityDir(cmd *cobra.Command) (string, error) {
	if v, _ := cmd.Flags().GetString(identityDirFlag); v != "" {
		return v, nil
	}
	if v := os.Getenv(envIdentityDir); v != "" {
		return v, nil
	}
	return identity.DefaultStoreDir()
}

func openIdentityStore(cmd *cobra.Command) (*identity.Store, error) {
	dir, err := resolveIdentityDir(cmd)
	if err != nil {
		return nil, err
	}
	return identity.NewStore(dir), nil
}

func newIdentityShowCmd() *cobra.Command {
	var (
		showPub bool
	)
	cmd := &cobra.Command{
		Use:   "show [<handle>]",
		Short: "Show one identity's details (default: the active default identity)",
		Long: `Without a handle, shows the default identity, generating one on
first call so the command never fails on a fresh install. Pass
--pub to print just the public-key PEM, ready for pasting into a
central node's authorized-keys file.`,
		Example: `  rex identity show
  rex identity show alice
  rex identity show --pub`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openIdentityStore(cmd)
			if err != nil {
				return err
			}

			handle := identity.DefaultHandle
			if len(args) == 1 {
				handle = identity.Handle(args[0])
			}

			// For the default handle, ensure-or-create so first
			// run never fails. For named handles, load strictly.
			var kp identity.Keypair
			if handle == identity.DefaultHandle {
				signer, err := identity.EnsureDefaultStoreSigner(store)
				if err != nil {
					return err
				}
				kp.Handle = signer.Handle()
				kp.Public = signer.PublicKey()
			} else {
				loaded, err := store.Load(handle)
				if err != nil {
					return err
				}
				kp = loaded
			}

			if showPub {
				pem, err := identity.MarshalPublicPEM(kp)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(pem)
				return err
			}

			fp := kp.Fingerprint()
			actor := (identity.Actor{Role: identity.RoleLocal, Fingerprint: fp}).String()

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"handle":      string(kp.Handle),
					"fingerprint": fp.String(),
					"actor":       actor,
				})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "handle:      %s\n", kp.Handle)
			fmt.Fprintf(out, "fingerprint: %s\n", fp)
			fmt.Fprintf(out, "actor:       %s\n", actor)
			fmt.Fprintf(out, "tip:         run `rex identity show --pub` to print the public key in PEM\n")
			return nil
		},
	}
	setRelated(cmd,
		"rex identity list",
		"rex remote bootstrap <name> <url> --token <token>",
		"rex remote show <name>",
	)
	cmd.Flags().BoolVar(&showPub, "pub", false, "print only the public-key PEM block")
	return cmd
}

func newIdentityListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List local identities",
		Long: `Lists the identities present in the local identity store with their
fingerprints.`,
		Example: `  rex identity list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openIdentityStore(cmd)
			if err != nil {
				return err
			}
			handles, err := store.List()
			if err != nil {
				return err
			}
			if jsonOutput(cmd) {
				rows := make([]map[string]any, 0, len(handles))
				for _, h := range handles {
					kp, err := store.Load(h)
					if err != nil {
						continue
					}
					rows = append(rows, map[string]any{
						"handle":      string(kp.Handle),
						"fingerprint": kp.Fingerprint().String(),
					})
				}
				return writeJSON(cmd, rows)
			}
			if len(handles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no identities yet (run `rex identity show` to create the default)")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "HANDLE\tFINGERPRINT")
			for _, h := range handles {
				kp, err := store.Load(h)
				if err != nil {
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\n", kp.Handle, kp.Fingerprint())
			}
			return tw.Flush()
		},
	}
	setRelated(cmd,
		"rex identity show",
		"rex remote bootstrap <name> <url> --token <token>",
		"rex remote show <name>",
	)
	return cmd
}
