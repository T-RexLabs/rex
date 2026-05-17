package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/asabla/rex/internal/central/server"
	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/identity"
	internalweb "github.com/asabla/rex/internal/web"
)

// postgresInviteRedeemer satisfies internalweb.InviteRedeemer
// for the central node. Composes the PostgresStore (the storage
// txn behind PeekInvite + RedeemInvite), the in-memory Keystore
// (so a freshly-redeemed key authenticates on the next request
// without waiting for a central restart), and the audit appender
// (emits identity.key_registered + org.member.joined per the
// 2026-05-17 amendment).
//
// Audit emission is best-effort, mirroring the existing
// adapter's behaviour: a failure logs but doesn't roll back the
// redeem (the redeem already committed by the time the appender
// runs).
type postgresInviteRedeemer struct {
	pg       *server.PostgresStore
	ks       *server.Keystore
	appender *server.PostgresAuditAppender
}

// newPostgresInviteRedeemer binds the adapter to its three
// collaborators. ks may be nil — the in-memory keystore overlay
// is then skipped (a freshly-redeemed key won't authenticate
// until the central restarts and reloads from authorized_keys).
// appender may be nil — audit events are silently dropped in
// that case (matches the auth-side appendAuthAudit shape).
func newPostgresInviteRedeemer(pg *server.PostgresStore, ks *server.Keystore, appender *server.PostgresAuditAppender) *postgresInviteRedeemer {
	return &postgresInviteRedeemer{pg: pg, ks: ks, appender: appender}
}

func (a *postgresInviteRedeemer) PeekInvite(token string) (internalweb.InviteSummary, error) {
	inv, err := a.pg.PeekInvite(context.Background(), token)
	if err != nil {
		return internalweb.InviteSummary{}, translateInviteErr(err)
	}
	return internalweb.InviteSummary{
		Token:     inv.Token,
		OrgID:     inv.OrgID,
		Role:      inv.Role,
		InvitedBy: inv.InvitedBy,
		ExpiresAt: inv.ExpiresAt,
	}, nil
}

func (a *postgresInviteRedeemer) RedeemInvite(req internalweb.RedeemRequest) (internalweb.RedeemOutcome, error) {
	ctx := context.Background()
	res, err := a.pg.RedeemInvite(ctx, req.Token, req.Handle, req.PublicKeyPEM)
	if err != nil {
		return internalweb.RedeemOutcome{}, translateInviteErr(err)
	}

	// Overlay the freshly-registered key into the in-memory
	// Keystore so the next inbound request can verify against
	// it without a central restart. We re-parse the PEM here
	// rather than threading the parsed key through the storage
	// layer because the public bytes are short and the parse
	// is cheap; centralising the parse in the storage layer
	// would require returning a non-portable ed25519 type
	// across the server/web boundary.
	if a.ks != nil && res.KeyRegistered {
		pub, perr := identity.ParsePublicPEM([]byte(req.PublicKeyPEM))
		if perr == nil {
			_, _ = a.ks.Add(req.Handle, pub)
		}
		// On parse failure we leave the keystore untouched
		// and trust the next restart's overlay to pick it
		// up — the row is already durable.
	}

	if a.appender != nil {
		if res.KeyRegistered {
			_ = a.appender.Append(ctx, audit.EventTypeIdentityKeyRegistered, audit.IdentityKeyRegisteredEvent{
				Fingerprint: res.Fingerprint,
				Handle:      res.Handle,
				Source:      "invite-redeem",
				InviteID:    res.InviteID,
			})
		}
		if res.MemberJoined {
			_ = a.appender.Append(ctx, audit.EventTypeOrgMemberJoined, audit.OrgMemberJoinedEvent{
				OrgID:       res.OrgID,
				Fingerprint: res.Fingerprint,
				Role:        res.Role,
				InviteID:    res.InviteID,
			})
		}
	}

	return internalweb.RedeemOutcome{
		OrgID:       res.OrgID,
		Fingerprint: res.Fingerprint,
		Role:        res.Role,
	}, nil
}

// translateInviteErr maps the server-side sentinels onto the
// internalweb-side sentinels so handlers can errors.Is without
// importing internal/central/server.
func translateInviteErr(err error) error {
	switch {
	case errors.Is(err, server.ErrInviteNotFound):
		return internalweb.ErrInviteNotFound
	case errors.Is(err, server.ErrInviteExpired):
		return internalweb.ErrInviteExpired
	case errors.Is(err, server.ErrInviteAlreadyRedeemed):
		return internalweb.ErrInviteAlreadyRedeemed
	}
	return fmt.Errorf("redeem: %w", err)
}
