package web

import (
	"time"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// AuditDefaultLimit is the default number of audit rows the /audit
// page renders when ?n= is omitted. Mirrors the local shell's
// historical default so the page looks the same on both shells.
const AuditDefaultLimit = 50

// AuditRow is one row in the /audit table (web-ui.SHARED.1
// audit_row partial). Mirrors the CLI's log-tail output.
type AuditRow struct {
	ID          string
	Timestamp   string
	Type        string
	Actor       string
	WorkspaceID string
}

// AuditProjection is the read-side surface the shared /audit
// handler queries. Local resolvers wrap a tail over
// `<Root>/.rex/events.log`; central resolvers wrap a tail over
// the central event store (central-node.DB.1) per
// web-ui.CENTRAL-LAYOUT.2.
type AuditProjection interface {
	// TailAudit returns the last `limit` audit-class events from
	// the workspace's log, in log order. Implementations that
	// already filter to audit-class events can ignore non-audit
	// types in their iteration; the shared FilterRecordsToAudit
	// helper does the filter for callers that pass it the raw
	// record stream.
	TailAudit(limit int) ([]AuditRow, error)
}

// OrgAuditProjection is the org-scoped sibling of AuditProjection:
// it surfaces the audit-class events the central node emitted
// against orgID (workspace-id is ignored; cross-workspace events
// like org.member.* + identity.key_registered + auth.* land here
// alongside any per-workspace audit rows whose org_id matches).
// Drives /orgs/<id>/audit (CENTRAL.3 — "every action runs through
// RBAC and writes audit entries"; that audit must be queryable
// per org).
type OrgAuditProjection interface {
	TailOrgAudit(orgID string, limit int) ([]AuditRow, error)
}

// FilterRecordsToAuditRows is the pure helper both shells'
// projections can use: walk records, keep audit-class entries,
// keep the last `limit` of them in a ring buffer.
//
// Implementations are free to skip this and do the filter inside
// their own iteration if it's cheaper (the local shell can stop
// early when the log is short; central reads everything from the
// store anyway). The helper exists so the shape conversion lives
// in one place.
func FilterRecordsToAuditRows(records []eventlog.Record, limit int) []AuditRow {
	if limit <= 0 {
		limit = AuditDefaultLimit
	}
	ring := make([]AuditRow, 0, limit)
	for _, rec := range records {
		if !audit.IsAuditEvent(rec.Type) {
			continue
		}
		row := AuditRow{
			ID:          rec.ID,
			Timestamp:   time.Unix(0, rec.Timestamp.Wall).UTC().Format(time.RFC3339),
			Type:        rec.Type,
			Actor:       rec.Actor,
			WorkspaceID: rec.WorkspaceID,
		}
		if len(ring) < limit {
			ring = append(ring, row)
		} else {
			ring = append(ring[1:], row)
		}
	}
	return ring
}
