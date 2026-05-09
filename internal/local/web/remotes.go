package web

import (
	"path/filepath"

	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// remoteDetailRow extends remoteRow (used on the home page) with
// fields the dedicated /remotes page surfaces.
type remoteDetailRow struct {
	Name             string
	URL              string
	Fingerprint      string
	AddedAt          string
	LastSeen         string
	Drafts           int
	NeedsRebase      bool
	LastConflictHead string
}

// IndicatorView returns the partial-friendly view of r.
func (r remoteDetailRow) IndicatorView() DraftIndicator {
	return DraftIndicator{
		Name: r.Name, Drafts: r.Drafts,
		NeedsRebase: r.NeedsRebase, LastConflictHead: r.LastConflictHead,
	}
}

// remotesData backs remotes.tmpl.
type remotesData struct {
	pageData
	Rows []remoteDetailRow
}

// loadRemotesData reads the per-user remotes registry plus the
// per-remote watermarks. Read-only: the web UI doesn't yet support
// remote add/remove/test (those stay CLI for v1).
func loadRemotesData(opts Options) (remotesData, error) {
	base := newPageDataFromOpts(opts)
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := remotesData{pageData: base}

	regPath, err := remotes.DefaultPath()
	if err != nil {
		return d, nil
	}
	reg, err := remotes.Load(regPath)
	if err != nil {
		return d, err
	}

	wms, _ := syncclient.ListWatermarks(opts.WorkspaceRoot)
	wmByName := make(map[string]syncclient.Watermark, len(wms))
	for _, w := range wms {
		wmByName[w.Remote] = w
	}

	logPath := filepath.Join(opts.WorkspaceRoot, ".rex", "events.log")

	for _, r := range reg.List() {
		row := remoteDetailRow{
			Name:        r.Name,
			URL:         r.URL,
			Fingerprint: r.Fingerprint,
		}
		if !r.AddedAt.IsZero() {
			row.AddedAt = r.AddedAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		if !r.LastSeen.IsZero() {
			row.LastSeen = r.LastSeen.UTC().Format("2006-01-02 15:04 UTC")
		}
		if wm, ok := wmByName[r.Name]; ok {
			count, _ := syncclient.CountEventsAfter(logPath, wm.LastAckedEventID)
			row.Drafts = count
			row.NeedsRebase = wm.NeedsRebase
			row.LastConflictHead = wm.LastConflictHead
		}
		d.Rows = append(d.Rows, row)
	}
	return d, nil
}
