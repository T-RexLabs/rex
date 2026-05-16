package web

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	syncclient "github.com/asabla/rex/internal/local/sync"
	internalweb "github.com/asabla/rex/internal/web"
)

// workspaceSummary is the minimal view of workspace.yaml the base
// template renders. Mirrors the cli's workspaceSettings shape
// without reaching into that package (web stays a leaf).
type workspaceSummary struct {
	ID        string
	Name      string
	State     string
	CreatedAt string
}

func loadWorkspaceSummary(root string) (*workspaceSummary, error) {
	body, err := os.ReadFile(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil {
		return nil, err
	}
	var ws workspaceSummary
	if err := yaml.Unmarshal(body, &struct {
		ID        *string `yaml:"id"`
		Name      *string `yaml:"name"`
		State     *string `yaml:"state"`
		CreatedAt *string `yaml:"created_at"`
	}{
		ID: &ws.ID, Name: &ws.Name, State: &ws.State, CreatedAt: &ws.CreatedAt,
	}); err != nil {
		return nil, err
	}
	return &ws, nil
}

// runRow is the local shell's alias for the shared RunRow type
// (web-ui.SHARED.1 run_row partial). Lifting it kept the local
// handler code on its existing identifier; new code should prefer
// internalweb.RunRow directly.
type runRow = internalweb.RunRow

// remoteRow is one entry in the remotes table on the home page.
//
// Carries the sync.DRAFT.2 rebase-needed signal so the shared
// draft_indicator partial can render the per-remote pill without a
// second lookup. The same fields drive the indicator on /remotes
// (remoteDetailRow embeds the equivalent state).
type remoteRow struct {
	Name             string
	Drafts           int
	LastSync         string
	NeedsRebase      bool
	LastConflictHead string
}

// DraftIndicator is the shape the draft_indicator partial expects.
// remoteRow / remoteDetailRow both expose IndicatorView() to return
// it, so any callsite can pipe a row directly into the partial.
type DraftIndicator struct {
	Name             string
	Drafts           int
	NeedsRebase      bool
	LastConflictHead string
}

// IndicatorView returns the partial-friendly view of r.
func (r remoteRow) IndicatorView() DraftIndicator {
	return DraftIndicator{
		Name: r.Name, Drafts: r.Drafts,
		NeedsRebase: r.NeedsRebase, LastConflictHead: r.LastConflictHead,
	}
}

// homeData is the page-specific payload for the / route.
type homeData struct {
	pageData
	SpecCount  int
	RunCount   int
	EventCount int
	RecentRuns []runRow
	Remotes    []remoteRow
}

// loadHomeData composes the home page's data from the same files
// the CLI commands read. Failures are tolerated per-section: a
// missing events.log yields zero events rather than a 500.
func loadHomeData(opts Options) (homeData, error) {
	base := newPageDataFromOpts(opts)
	ws, err := loadWorkspaceSummary(opts.WorkspaceRoot)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return homeData{}, fmt.Errorf("web: read workspace.yaml: %w", err)
	}
	base.Workspace = ws

	d := homeData{pageData: base}
	d.SpecCount = countSpecs(opts.WorkspaceRoot)
	d.EventCount = countEvents(opts.WorkspaceRoot)

	runs, _ := loadRunRows(opts.WorkspaceRoot)
	d.RecentRuns = runs
	if len(runs) > 5 {
		d.RecentRuns = runs[:5]
	}
	d.RunCount = len(runs)

	d.Remotes = loadRemoteRows(opts.WorkspaceRoot)
	return d, nil
}

func countSpecs(root string) int {
	entries, err := os.ReadDir(filepath.Join(root, ".rex", "specs"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
			n++
		}
	}
	return n
}

func countEvents(root string) int {
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		return 0
	}
	defer r.Close()
	n := 0
	for {
		_, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return n
		}
		n++
	}
	return n
}

// loadRunRows reads events.log into a slice and delegates the
// fold to internalweb.FoldRecordsToRunRows so the local and
// central shells share the projection logic. A missing log
// returns an empty slice rather than an error.
func loadRunRows(root string) ([]runRow, error) {
	records, err := readEventsLog(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		return nil, err
	}
	return internalweb.FoldRecordsToRunRows(records)
}

// readEventsLog slurps the local events.log into a slice of
// records. A missing log returns (nil, nil); other I/O failures
// bubble out. Used by every local projection that needs to feed
// the shared fold helpers in internalweb — keeping the slurp in
// one place avoids subtle io-layer drift across handlers.
func readEventsLog(path string) ([]eventlog.Record, error) {
	r, err := eventlog.OpenReader(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()
	var out []eventlog.Record
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// loadRemoteRows enumerates per-remote watermark files and reports
// draft counts. Best-effort: a missing remotes registry or watermark
// directory yields an empty slice.
func loadRemoteRows(root string) []remoteRow {
	wms, err := syncclient.ListWatermarks(root)
	if err != nil {
		return nil
	}
	out := make([]remoteRow, 0, len(wms))
	logPath := filepath.Join(root, ".rex", "events.log")
	for _, wm := range wms {
		count, _ := syncclient.CountEventsAfter(logPath, wm.LastAckedEventID)
		last := "—"
		if !wm.AckedAt.IsZero() {
			last = wm.AckedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, remoteRow{
			Name:             wm.Remote,
			Drafts:           count,
			LastSync:         last,
			NeedsRebase:      wm.NeedsRebase,
			LastConflictHead: wm.LastConflictHead,
		})
	}
	return out
}
