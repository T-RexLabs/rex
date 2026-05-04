package web

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// auditRow is one row in the /audit table. Mirrors what the CLI's
// log tail prints.
type auditRow struct {
	ID          string
	Timestamp   string
	Type        string
	Actor       string
	WorkspaceID string
}

// auditData backs audit.tmpl.
type auditData struct {
	pageData
	Rows  []auditRow
	Limit int
}

const auditDefaultLimit = 50

// loadAuditRows scans events.log and returns the last `limit`
// audit-class events. Mirrors readAndFilter in cli/log.go but stays
// inside this package — the two surfaces are allowed to evolve at
// their own pace.
func loadAuditRows(opts Options, limit int) (auditData, error) {
	if limit <= 0 {
		limit = auditDefaultLimit
	}

	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := auditData{pageData: base, Limit: limit}

	logPath := filepath.Join(opts.WorkspaceRoot, ".rex", "events.log")
	r, err := eventlog.OpenReader(logPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return d, nil
		}
		return d, err
	}
	defer r.Close()

	ring := make([]auditRow, 0, limit)
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return d, err
		}
		if !audit.IsAuditEvent(rec.Type) {
			continue
		}
		row := auditRow{
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
	d.Rows = ring
	return d, nil
}
