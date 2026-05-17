package main

import (
	"context"
	"errors"

	"github.com/asabla/rex/internal/central/server"
	internalweb "github.com/asabla/rex/internal/web"
)

// postgresOrgsAdapter satisfies internalweb.OrgsProjection by
// calling the central PostgresStore's org methods. Lives in
// cmd/rex-central because internal/central/web does not import
// internal/central/server (the web package stays a leaf).
//
// Read methods: LookupOrg, ListMembers, RoleFor.
// Mutation methods: ChangeMemberRole, RemoveMember. The
// outstanding admin operation is invite issuance — the
// org_invites schema is already there but the redeem flow needs
// its own design pass (token format, /auth/invite endpoint
// shape, web form) so it ships in a follow-up.
type postgresOrgsAdapter struct {
	pg *server.PostgresStore
}

func newPostgresOrgsAdapter(pg *server.PostgresStore) *postgresOrgsAdapter {
	return &postgresOrgsAdapter{pg: pg}
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
// PostgresStore and translates the server-package sentinel into
// the web-side ErrUnknownMembership so handlers can errors.Is
// without importing internal/central/server.
func (a *postgresOrgsAdapter) ChangeMemberRole(orgID, fingerprint, newRole string) (string, error) {
	prior, err := a.pg.ChangeMemberRole(context.Background(), orgID, fingerprint, newRole)
	if errors.Is(err, server.ErrUnknownMembership) {
		return "", internalweb.ErrUnknownMembership
	}
	return prior, err
}

// RemoveMember forwards to the central server's PostgresStore
// with the same sentinel translation as ChangeMemberRole.
func (a *postgresOrgsAdapter) RemoveMember(orgID, fingerprint string) (string, error) {
	prior, err := a.pg.RemoveMember(context.Background(), orgID, fingerprint)
	if errors.Is(err, server.ErrUnknownMembership) {
		return "", internalweb.ErrUnknownMembership
	}
	return prior, err
}
