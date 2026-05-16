package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	internalweb "github.com/asabla/rex/internal/web"
)

// EventReader is the read-side subset of the central event Store
// the web shell's projections depend on. Defined here so the web
// package doesn't import internal/central/server; cmd/rex-central
// binds the central server's Store to this interface at wireup
// time. Mirrors the Since method of server.Store (sync.API.3 +
// central-node.DB.1).
type EventReader interface {
	Since(ctx context.Context, cursor string) ([]eventlog.Record, error)
}

// centralRunsListProjection satisfies internalweb.RunsListProjection
// by reading the central event store and folding records via the
// shared helper (web-ui.CENTRAL-LAYOUT.2). v1 single-workspace
// limitation per centralWorkspaceResolver — every workspace id
// gets the same projection until the multi-workspace store
// refactor lands.
type centralRunsListProjection struct {
	events EventReader
	ctx    context.Context
}

func newCentralRunsListProjection(ctx context.Context, events EventReader) centralRunsListProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralRunsListProjection{events: events, ctx: ctx}
}

func (p centralRunsListProjection) ListRuns() ([]internalweb.RunRow, error) {
	if p.events == nil {
		return nil, nil
	}
	records, err := p.events.Since(p.ctx, "")
	if err != nil {
		return nil, fmt.Errorf("central runs: read events: %w", err)
	}
	return internalweb.FoldRecordsToRunRows(records)
}

// centralRunDetailProjection satisfies
// internalweb.RunDetailProjection by reading the central event
// store and folding records that mention runID into the shared
// terminal-state RunDetail. Live-tail + permission flow are out
// of scope — the central run-detail template renders a banner
// for non-terminal runs (Decision B in the 2026-05-16 amendment).
type centralRunDetailProjection struct {
	events EventReader
	ctx    context.Context
}

func newCentralRunDetailProjection(ctx context.Context, events EventReader) centralRunDetailProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralRunDetailProjection{events: events, ctx: ctx}
}

func (p centralRunDetailProjection) GetRun(runID string) (internalweb.RunDetail, bool, error) {
	if p.events == nil {
		return internalweb.RunDetail{}, false, nil
	}
	records, err := p.events.Since(p.ctx, "")
	if err != nil {
		return internalweb.RunDetail{}, false, fmt.Errorf("central run detail: read events: %w", err)
	}
	return internalweb.FoldRecordsToRunDetail(records, runID)
}

// centralRunsListData backs runs_list.tmpl on the central shell.
// Mirrors the local envelope's field names so the shared template
// renders identically. CanStartRuns is false because central-side
// execution is deferred to v1.5.
type centralRunsListData struct {
	centralPageData
	Runs         []internalweb.RunRow
	SpecFilter   string
	CanStartRuns bool
}

// centralRunDetailData backs runs_central_detail.tmpl (the
// minimal terminal-state-only template that's central-only in v1).
type centralRunDetailData struct {
	centralPageData
	Detail internalweb.RunDetail
}

// handleRunsList is GET /orgs/<org>/workspaces/<ws>/runs.
// Pulls runs via the workspace's RunsListProjection, applies the
// optional ?spec=<token> filter using the shared helper, and
// renders runs_list.tmpl with the central envelope.
func (s *Server) handleRunsList(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	wsID := r.PathValue("ws")
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	if ws.Runs == nil {
		http.Error(w, "central web: workspace has no runs projection", http.StatusServiceUnavailable)
		return
	}
	rows, err := ws.Runs.ListRuns()
	if err != nil {
		http.Error(w, "central web: list runs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	specFilter := r.URL.Query().Get("spec")
	if specFilter != "" {
		filtered := rows[:0]
		for _, row := range rows {
			if internalweb.MatchesSpecFilter(row, specFilter) {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	data := centralRunsListData{
		centralPageData: s.pageData(orgID, wsID, "runs"),
		Runs:            rows,
		SpecFilter:      specFilter,
		// Central-side execution is deferred (overview.SCOPE.1);
		// hide the "start a run" affordance.
		CanStartRuns: false,
	}
	s.renderer.Render(w, r, "runs_list.tmpl", data)
}

// handleRunDetail is GET /orgs/<org>/workspaces/<ws>/runs/<id>.
// Renders the central-only runs_central_detail.tmpl — the rich
// frame-view + permission flow is local-only because central has
// no in-flight event source in v1. Non-terminal runs surface a
// "live tail not available on central in v1" banner (template
// gates this on .Detail.Terminal).
func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	wsID := r.PathValue("ws")
	runID := r.PathValue("id")
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	if ws.RunDetail == nil {
		http.Error(w, "central web: workspace has no run-detail projection", http.StatusServiceUnavailable)
		return
	}
	detail, found, err := ws.RunDetail.GetRun(runID)
	if err != nil {
		http.Error(w, "central web: load run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	data := centralRunDetailData{
		centralPageData: s.pageData(orgID, wsID, "runs"),
		Detail:          detail,
	}
	s.renderer.Render(w, r, "runs_central_detail.tmpl", data)
}
