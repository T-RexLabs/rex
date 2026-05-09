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
	Name       string
	Kind       string
	Status     runner.RunStatus
	StartedAt  string
	EndedAt    string
	Duration   string
	NodeEvents int
	// SpecRefs and FromTask are recorded on the run.started event
	// when the run was launched from a spec recipe (execution.RUN.1.1).
	// Surfaced so the runs list can filter and badge.
	SpecRefs []string
	FromTask string
}

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

// loadRunRows folds events.log into per-run summaries (run_id,
// status, started_at, node_event_count) using the shared
// runner.RunSummary helper.
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

	by := map[string]*runner.RunSummary{}
	kinds := map[string]string{} // run_id → "shell" | "harness"
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
		// Any harness.frame event for a run is sufficient evidence
		// that the run is harness-driven; shell runs never produce
		// frames. We fold this lookup into the same scan so we
		// don't read events.log twice.
		if hf, ok := decoded.(runner.HarnessFrameEvent); ok {
			kinds[hf.RunID] = "harness"
			continue
		}
		var probe runner.RunSummary
		if !probe.FoldEvent(decoded) {
			continue
		}
		s, ok := by[probe.RunID]
		if !ok {
			s = &runner.RunSummary{}
			by[probe.RunID] = s
		}
		s.FoldEvent(decoded)
	}

	out := make([]runRow, 0, len(by))
	for _, s := range by {
		kind := kinds[s.RunID]
		if kind == "" {
			kind = "shell"
		}
		row := runRow{
			RunID:      s.RunID,
			Name:       runner.FriendlyName(s.RunID),
			Kind:       kind,
			Status:     s.EffectiveStatus(),
			StartedAt:  s.StartedAt.UTC().Format(time.RFC3339),
			NodeEvents: s.NodeEvents,
			SpecRefs:   append([]string(nil), s.SpecRefs...),
			FromTask:   s.FromTask,
		}
		if !s.EndedAt.IsZero() {
			row.EndedAt = s.EndedAt.UTC().Format(time.RFC3339)
			row.Duration = s.EndedAt.Sub(s.StartedAt).Truncate(time.Millisecond).String()
		}
		out = append(out, row)
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
