package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// newRemoteJoinCmd implements `rex remote join <name> <url>
// --invite <token>` — the recipient-side CLI counterpart to
// `rex remote add` (handshake + TOFU) plus the invite redeem
// flow shipped on the central node
// (identity-and-trust.AUTH.2.1).
//
// Flow:
//
//  1. Ensure-or-create the local identity (same default-key
//     path as `rex identity show --pub`).
//  2. Handshake against <url> (GET /sync/state) so the
//     fingerprint + protocol version are known before any
//     destructive action — this also acts as the TOFU
//     confirmation step (sync.BOOT.1.1).
//  3. Peek the invite (GET /invites/<token>) so a bad token
//     fails fast before we transmit a public key.
//  4. Confirm trust (or accept silently with --yes).
//  5. POST /invites/redeem with the invite token + local
//     handle + public key PEM.
//  6. Register the remote in ~/.config/rex/remotes.toml with
//     the just-confirmed fingerprint.
//
// On any step's failure the local registry is not modified.
func newRemoteJoinCmd() *cobra.Command {
	var (
		inviteToken string
		autoYes     bool
	)
	cmd := &cobra.Command{
		Use:   "join <name> <url>",
		Short: "Redeem an invite, register a remote in one shot",
		Long: `Combines ` + "`rex remote add`" + ` (handshake + TOFU confirmation) with
the invite-redeem flow shipped on the central node. Generates or
loads the default local identity, contacts the remote at <url>,
peeks the invite to fail fast on a bad token, prompts to confirm
the server's fingerprint, and POSTs the local public key against
` + "`POST /invites/redeem`" + ` (no auth required — the invite token IS
the credential).

On success the remote is registered locally with the
just-confirmed fingerprint. On any failure (network down, bad
token, declined confirmation) the local registry is not
modified.`,
		Example: `  rex remote join primary https://central.example.invalid --invite "$TOKEN"
  rex remote join primary https://central.example.invalid --invite "$TOKEN" --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]
			reg, path, err := loadRegistry(cmd)
			if err != nil {
				return err
			}
			if _, exists := reg.Get(name); exists {
				return fmt.Errorf("remote %q is already registered; pick a different name or `rex remote remove %s` first", name, name)
			}

			signer, err := loadOrCreateDefaultSigner(cmd)
			if err != nil {
				return fmt.Errorf("join %q: load identity: %w", name, err)
			}
			pem, err := identity.MarshalPublicPEM(identity.Keypair{
				Handle: signer.Handle(), Public: signer.PublicKey(),
			})
			if err != nil {
				return fmt.Errorf("join %q: export public key: %w", name, err)
			}

			ctx := commandContext(cmd)
			client := syncclient.NewClient(url)
			state, err := client.State(ctx)
			if err != nil {
				return fmt.Errorf("join %q: contact %q: %w", name, url, err)
			}
			inv, err := client.PeekInvite(ctx, inviteToken)
			if err != nil {
				return translateJoinInviteErr(err)
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"server fingerprint: %s\nprotocol version:   %d\norg:                %s\nrole:               %s\nexpires:            %s\n",
				state.Fingerprint, state.ProtocolVersion, inv.OrgID, inv.Role, inv.ExpiresAt,
			)
			if !autoYes {
				ok, perr := confirmTrust(cmd, name)
				if perr != nil {
					return perr
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "declined; remote not registered and invite not redeemed")
					return nil
				}
			}

			out, err := client.RedeemInvite(ctx, inviteToken, string(signer.Handle()), string(pem))
			if err != nil {
				return translateJoinInviteErr(err)
			}

			entry := remotes.Remote{
				Name:        name,
				URL:         url,
				Fingerprint: state.Fingerprint,
				LastSeen:    time.Now().UTC(),
			}
			if err := reg.Add(entry); err != nil {
				return fmt.Errorf("join %q: register remote: %w", name, err)
			}
			if err := remotes.Save(path, reg); err != nil {
				return fmt.Errorf("join %q: save registry: %w", name, err)
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name":         name,
					"url":          url,
					"fingerprint":  state.Fingerprint,
					"org_id":       out.OrgID,
					"role":         out.Role,
					"member_as_fp": out.Fingerprint,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"joined %q as %s in org %s (fingerprint=%s); remote registered\n",
				name, out.Role, out.OrgID, out.Fingerprint,
			)
			return nil
		},
	}
	setRelated(cmd,
		"rex remote add <name> <url>",
		"rex remote show <name>",
		"rex identity show --pub",
	)
	cmd.Flags().StringVar(&inviteToken, "invite", "", "invite token (issued by an admin via the central web UI's /orgs/<id>/members page)")
	if err := cmd.MarkFlagRequired("invite"); err != nil {
		_ = err
	}
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "accept the observed fingerprint without prompting")
	addRemoteSharedFlags(cmd)
	return cmd
}

// translateJoinInviteErr surfaces the sync client's invite
// sentinels with user-facing wording suitable for the CLI. Other
// errors flow through unchanged.
func translateJoinInviteErr(err error) error {
	switch {
	case errors.Is(err, syncclient.ErrInviteNotFound):
		return fmt.Errorf("invite token not recognised; ask the issuing admin to re-issue")
	case errors.Is(err, syncclient.ErrInviteExpired):
		return fmt.Errorf("invite expired; ask the issuing admin to re-issue (TTL is 7 days)")
	case errors.Is(err, syncclient.ErrInviteAlreadyRedeemed):
		return fmt.Errorf("invite has already been redeemed; ask for a fresh invite")
	}
	return err
}
