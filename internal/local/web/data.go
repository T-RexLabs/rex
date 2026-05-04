package web

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
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

// runRow is one entry in the recent-runs table on the home page.
type runRow struct {
	RunID      string
	Status     runner.RunStatus
	StartedAt  string
	NodeEvents int
}

// remoteRow is one entry in the remotes table on the home page.
type remoteRow struct {
	Name     string
	Drafts   int
	LastSync string
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
	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
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

// loadRunRows folds events.log into per-run summaries (run_id,
// status, started_at, node_event_count). Mirrors the CLI's
// readRunSummaries logic but stays inside this package so web/cli
// can each iterate at their own pace.
func loadRunRows(root string) ([]runRow, error) {
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()

	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	type acc struct {
		runID      string
		status     runner.RunStatus
		startedAt  time.Time
		endedAt    time.Time
		nodeEvents int
	}
	by := map[string]*acc{}
	get := func(id string) *acc {
		a, ok := by[id]
		if !ok {
			a = &acc{runID: id}
			by[id] = a
		}
		return a
	}
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		decoded, err := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if errors.Is(err, event.ErrSkipUnknownType) {
			continue
		}
		if err != nil {
			return nil, err
		}
		switch ev := decoded.(type) {
		case runner.RunStartedEvent:
			a := get(ev.RunID)
			if a.startedAt.IsZero() {
				a.startedAt = ev.StartedAt
			}
		case runner.RunCompletedEvent:
			a := get(ev.RunID)
			a.status = runner.RunStatusCompleted
			a.endedAt = ev.CompletedAt
		case runner.RunCancelledEvent:
			a := get(ev.RunID)
			a.status = runner.RunStatusCancelled
			a.endedAt = ev.CancelledAt
		case runner.RunAbortedEvent:
			a := get(ev.RunID)
			a.status = runner.RunStatusAborted
			a.endedAt = ev.AbortedAt
		case runner.NodeStartedEvent:
			get(ev.RunID).nodeEvents++
		case runner.NodeSucceededEvent:
			get(ev.RunID).nodeEvents++
		case runner.NodeFailedEvent:
			get(ev.RunID).nodeEvents++
		case runner.NodeRetriedEvent:
			get(ev.RunID).nodeEvents++
		}
	}

	out := make([]runRow, 0, len(by))
	for _, a := range by {
		status := a.status
		if status == "" {
			status = runner.RunStatusRunning
		}
		out = append(out, runRow{
			RunID:      a.runID,
			Status:     status,
			StartedAt:  a.startedAt.UTC().Format(time.RFC3339),
			NodeEvents: a.nodeEvents,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
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
		out = append(out, remoteRow{Name: wm.Remote, Drafts: count, LastSync: last})
	}
	return out
}

// _ keeps the remotes package referenced for when /remotes lands —
// avoids "imported but not used" while the home page only consults
// watermarks. Will be replaced with a real call once the route is
// wired.
var _ = remotes.FileName
