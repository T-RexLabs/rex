package main

import (
	"context"
	"errors"

	"github.com/asabla/rex/internal/central/server"
	"github.com/asabla/rex/internal/core/audit"
	internalweb "github.com/asabla/rex/internal/web"
)

// postgresOrgsAdapter satisfies internalweb.OrgsProjection by
// calling the central PostgresStore's org methods, and emits
// audit events for every mutation through the bound appender
// (CENTRAL.3 — "every action runs through RBAC and writes audit
// entries"). Lives in cmd/rex-central because
// internal/central/web does not import internal/central/server
// (the web package stays a leaf).
//
// Read methods: LookupOrg, ListMembers, RoleFor.
// Mutation methods: ChangeMemberRole, RemoveMember. The
// outstanding admin operation is invite issuance — the
// org_invites schema is already there but the redeem flow needs
// its own design pass (token format, /auth/invite endpoint
// shape, web form) so it ships in a follow-up.
type postgresOrgsAdapter struct {
	pg       *server.PostgresStore
	appender *server.PostgresAuditAppender
}

// newPostgresOrgsAdapter binds the adapter to a PostgresStore +
// an audit appender. The appender may be nil — in that case
// mutations succeed but no audit events are emitted (matches
// the existing best-effort semantics auth.go's appendAuthAudit
// uses).
func newPostgresOrgsAdapter(pg *server.PostgresStore, appender *server.PostgresAuditAppender) *postgresOrgsAdapter {
	return &postgresOrgsAdapter{pg: pg, appender: appender}
}

func (a *postgresOrgsAdapter) LookupOrg(orgID string) (internalweb.OrgSummary, bool, error) {
	org, err := a.pg.LookupOrgByID(context.Background(), orgID)
	if err != nil {
		// Treat any error (not-found, transient) as "not found"
		// for the web surface — the per-org pages already 404
		// on (_, false, nil); a deeper diagnostic lives in
		// server logs.
		return internalweb.OrgSummary{}, false, nil
	}
	return internalweb.OrgSummary{
		ID:          org.ID,
		Name:        org.Name,
		DisplayName: org.DisplayName,
		CreatedAt:   org.CreatedAt,
	}, true, nil
}

func (a *postgresOrgsAdapter) ListOrgsForFingerprint(fingerprint string) ([]internalweb.OrgSummary, error) {
	orgs, err := a.pg.ListOrgsForFingerprint(context.Background(), fingerprint)
	if err != nil {
		return nil, err
	}
	out := make([]internalweb.OrgSummary, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, internalweb.OrgSummary{
			ID:          o.ID,
			Name:        o.Name,
			DisplayName: o.DisplayName,
			CreatedAt:   o.CreatedAt,
		})
	}
	return out, nil
}

func (a *postgresOrgsAdapter) ListMembers(orgID string) ([]internalweb.MembershipRow, error) {
	members, err := a.pg.ListMembersForOrg(context.Background(), orgID)
	if err != nil {
		return nil, err
	}
	out := make([]internalweb.MembershipRow, 0, len(members))
	for _, m := range members {
		out = append(out, internalweb.MembershipRow{
			Fingerprint: m.Fingerprint,
			Role:        m.Role,
			JoinedAt:    m.JoinedAt,
		})
	}
	return out, nil
}

// RoleFor returns the role the fingerprint holds in orgID, or
// "" when no membership exists. Backs the web shell's per-handler
// requireOrgMember check (identity-and-trust.RBAC.1).
func (a *postgresOrgsAdapter) RoleFor(orgID, fingerprint string) (string, error) {
	return a.pg.RoleFor(context.Background(), orgID, fingerprint)
}

// ChangeMemberRole forwards to the central server's
// PostgresStore and emits an org.member.role_changed audit
// event on a successful role transition. Translates the
// server-package sentinel into the web-side
// ErrUnknownMembership so handlers can errors.Is without
// importing internal/central/server.
//
// changerFingerprint carries the authenticated caller's
// identity (the session gate stamped it on the request
// context; the handler pulled it back via SessionFromContext
// and threaded it through here). Empty changerFingerprint still
// emits — the audit ChangedBy field is just empty in that case
// (matches the spec's "every action writes an audit entry"
// intent even when the caller's identity is unknown to the
// gate, which shouldn't happen in production but the dev
// pass-through path allows).
//
// Audit emission is best-effort: a failure logs but doesn't
// roll back the role change. PostgresAuditAppender already has
// the same shape, mirroring the existing appendAuthAudit
// behaviour on the central server.
func (a *postgresOrgsAdapter) ChangeMemberRole(orgID, fingerprint, newRole, changerFingerprint string) (string, error) {
	ctx := server.WithOrgID(context.Background(), orgID)
	prior, err := a.pg.ChangeMemberRole(ctx, orgID, fingerprint, newRole)
	if errors.Is(err, server.ErrUnknownMembership) {
		return "", internalweb.ErrUnknownMembership
	}
	if err != nil {
		return "", err
	}
	if prior != newRole && a.appender != nil {
		_ = a.appender.Append(ctx, audit.EventTypeOrgMemberRoleChanged, audit.OrgMemberRoleChangedEvent{
			OrgID:       orgID,
			Fingerprint: fingerprint,
			FromRole:    prior,
			ToRole:      newRole,
			ChangedBy:   changerFingerprint,
		})
	}
	return prior, nil
}

// IssueInvite forwards to PostgresStore.IssueInvite and emits
// an org.member.invited audit event on success. The token never
// rides through the audit body — InviteID is the bookkeeping
// reference for the eventual redeem entry.
func (a *postgresOrgsAdapter) IssueInvite(orgID, inviter, role string) (internalweb.InviteRow, error) {
	ctx := server.WithOrgID(context.Background(), orgID)
	inv, err := a.pg.IssueInvite(ctx, orgID, inviter, role)
	if err != nil {
		return internalweb.InviteRow{}, err
	}
	if a.appender != nil {
		_ = a.appender.Append(ctx, audit.EventTypeOrgMemberInvited, audit.OrgMemberInvitedEvent{
			OrgID:    orgID,
			InviteID: inv.ID,
			Role:     inv.Role,
			Inviter:  inviter,
		})
	}
	return internalweb.InviteRow{
		ID:        inv.ID,
		Token:     inv.Token,
		Role:      inv.Role,
		InvitedBy: inv.InvitedBy,
		ExpiresAt: inv.ExpiresAt,
	}, nil
}

// ListPendingInvites forwards to PostgresStore and maps the
// server-side Invite shape onto the web-side InviteRow.
func (a *postgresOrgsAdapter) ListPendingInvites(orgID string) ([]internalweb.InviteRow, error) {
	ctx := server.WithOrgID(context.Background(), orgID)
	invs, err := a.pg.ListPendingInvites(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]internalweb.InviteRow, 0, len(invs))
	for _, inv := range invs {
		out = append(out, internalweb.InviteRow{
			ID:        inv.ID,
			Token:     inv.Token,
			Role:      inv.Role,
			InvitedBy: inv.InvitedBy,
			ExpiresAt: inv.ExpiresAt,
		})
	}
	return out, nil
}

// RemoveMember forwards to the central server's PostgresStore
// with the same sentinel translation as ChangeMemberRole, plus
// an org.member.removed audit emission on success.
func (a *postgresOrgsAdapter) RemoveMember(orgID, fingerprint, removerFingerprint string) (string, error) {
	ctx := server.WithOrgID(context.Background(), orgID)
	prior, err := a.pg.RemoveMember(ctx, orgID, fingerprint)
	if errors.Is(err, server.ErrUnknownMembership) {
		return "", internalweb.ErrUnknownMembership
	}
	if err != nil {
		return "", err
	}
	if a.appender != nil {
		_ = a.appender.Append(ctx, audit.EventTypeOrgMemberRemoved, audit.OrgMemberRemovedEvent{
			OrgID:       orgID,
			Fingerprint: fingerprint,
			PriorRole:   prior,
			RemovedBy:   removerFingerprint,
		})
	}
	return prior, nil
}
