package web

import (
	"path/filepath"

	internalweb "github.com/asabla/rex/internal/web"
)

// auditRow is the local shell's alias for the shared AuditRow.
type auditRow = internalweb.AuditRow

// auditData backs audit.tmpl.
type auditData struct {
	pageData
	Rows []auditRow
	// Limit is the max row count the page asked for; surfaced
	// in the meta line.
	Limit int
	// Source labels the underlying event source in the meta
	// line. Local sets it to ".rex/events.log"; central sets it
	// to "the central event store". Empty hides the source
	// hint entirely (tests that exercise the projection
	// directly don't bother to set it).
	Source string
}

const auditDefaultLimit = internalweb.AuditDefaultLimit

// loadAuditRows delegates the audit-event filter to the shared
// helper in internalweb so local and central produce identical
// row shapes (web-ui.SHARED.2).
func loadAuditRows(opts Options, limit int) (auditData, error) {
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	base := newPageDataFromOpts(opts)
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := auditData{pageData: base, Limit: limit, Source: ".rex/events.log"}
	records, err := readEventsLog(filepath.Join(opts.WorkspaceRoot, ".rex", "events.log"))
	if err != nil {
		return d, err
	}
	d.Rows = internalweb.FilterRecordsToAuditRows(records, limit)
	return d, nil
}
