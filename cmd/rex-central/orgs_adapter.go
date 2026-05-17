package main

import (
	"context"

	"github.com/asabla/rex/internal/central/server"
	internalweb "github.com/asabla/rex/internal/web"
)

// postgresOrgsAdapter satisfies internalweb.OrgsProjection by
// calling the central PostgresStore's read-side org methods.
// Lives in cmd/rex-central because internal/central/web does not
// import internal/central/server (the web package stays a leaf).
//
// Mutating admin operations (invite, role change, removal) are
// tracked under central-node.RBAC-SVR.1 and are deliberately
// absent from this interface — the central org-admin pages
// surface them as "admin REST API pending" until that work
// ships.
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
