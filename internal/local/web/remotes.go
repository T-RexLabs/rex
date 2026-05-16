package web

import (
	"path/filepath"
	"sort"

	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
	internalweb "github.com/asabla/rex/internal/web"
)

// remoteDetailRow aliases the shared row type for legacy refs
// inside this package.
type remoteDetailRow = internalweb.RemoteRow

// remotesData backs remotes.tmpl.
type remotesData struct {
	pageData
	Rows []remoteDetailRow
	// Source labels the underlying registry the rows came from.
	// Local sets "~/.config/rex/remotes.toml"; central sets
	// "the workspace's synced .rex/remotes.toml".
	Source string
	// AddCmd is the inline hint command the empty-state surfaces.
	// Local shows `rex remote add ...`; central leaves it empty
	// because remote management on central is local-only in v1.
	AddCmd string
}

// localRemotesProjection satisfies internalweb.RemotesProjection
// by reading the per-machine remotes registry + per-remote sync
// watermarks the local node has accumulated.
type localRemotesProjection struct{ root string }

func (l localRemotesProjection) ListRemotes() ([]internalweb.RemoteRow, error) {
	regPath, err := remotes.DefaultPath()
	if err != nil {
		return nil, nil
	}
	reg, err := remotes.Load(regPath)
	if err != nil {
		return nil, err
	}
	wms, _ := syncclient.ListWatermarks(l.root)
	wmByName := make(map[string]syncclient.Watermark, len(wms))
	for _, w := range wms {
		wmByName[w.Remote] = w
	}
	logPath := filepath.Join(l.root, ".rex", "events.log")
	rows := make([]internalweb.RemoteRow, 0, len(reg.List()))
	for _, r := range reg.List() {
		row := internalweb.RemoteRow{
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
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// loadRemotesData composes the local /remotes page envelope.
func loadRemotesData(opts Options) (remotesData, error) {
	base := newPageDataFromOpts(opts)
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := remotesData{
		pageData: base,
		Source:   "~/.config/rex/remotes.toml",
		AddCmd:   "rex remote add <name> <url>",
	}
	rows, err := localRemotesProjection{root: opts.WorkspaceRoot}.ListRemotes()
	if err != nil {
		return d, err
	}
	d.Rows = rows
	return d, nil
}
