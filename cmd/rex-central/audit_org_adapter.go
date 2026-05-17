package main

import (
	"context"
	"fmt"

	"github.com/asabla/rex/internal/central/server"
	internalweb "github.com/asabla/rex/internal/web"
)

// postgresOrgAuditProjection satisfies
// internalweb.OrgAuditProjection by reading the central event
// store with explicit org scope and filtering to audit-class
// rows via the shared helper.
//
// Lives in cmd/rex-central because internal/central/web does not
// import internal/central/server (the web package stays a leaf).
// The wireup binds it only when --db is on; the in-memory dev
// path has no org concept, so /orgs/<id>/audit responds 503
// there.
type postgresOrgAuditProjection struct {
	pg *server.PostgresStore
}

func newPostgresOrgAuditProjection(pg *server.PostgresStore) *postgresOrgAuditProjection {
	return &postgresOrgAuditProjection{pg: pg}
}

func (p *postgresOrgAuditProjection) TailOrgAudit(orgID string, limit int) ([]internalweb.AuditRow, error) {
	if orgID == "" {
		return nil, fmt.Errorf("server: TailOrgAudit requires orgID")
	}
	ctx := server.WithOrgID(context.Background(), orgID)
	records, err := p.pg.Since(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("server: org audit read: %w", err)
	}
	return internalweb.FilterRecordsToAuditRows(records, limit), nil
}
