package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	internalweb "github.com/asabla/rex/internal/web"
)

// centralAuditProjection satisfies internalweb.AuditProjection
// by reading the central event store and filtering audit-class
// events via the shared helper (web-ui.CENTRAL-LAYOUT.2). The
// projection reads the entire store on every request — fine for
// v1 in-process MemoryStore; a future bound-by-cursor optimisation
// can land alongside the multi-workspace refactor without changing
// the handler call sites.
type centralAuditProjection struct {
	events      EventReader
	workspaceID string
	ctx         context.Context
}

func newCentralAuditProjection(ctx context.Context, events EventReader, workspaceID string) centralAuditProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralAuditProjection{events: events, workspaceID: workspaceID, ctx: ctx}
}

func (p centralAuditProjection) TailAudit(limit int) ([]internalweb.AuditRow, error) {
	records, err := readWorkspaceEvents(p.ctx, p.events, p.workspaceID)
	if err != nil {
		return nil, fmt.Errorf("central audit: read events: %w", err)
	}
	return internalweb.FilterRecordsToAuditRows(records, limit), nil
}

// centralAuditData backs audit.tmpl on the central shell. Field
// names mirror the local envelope so the shared template renders
// identically.
type centralAuditData struct {
	centralPageData
	Rows   []internalweb.AuditRow
	Limit  int
	Source string
}

// handleAudit is GET /orgs/<org>/workspaces/<ws>/audit. Honours
// ?n=<limit> the same way the local shell does; otherwise falls
// back to the shared default.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
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
	if ws.Audit == nil {
		http.Error(w, "central web: workspace has no audit projection", http.StatusServiceUnavailable)
		return
	}
	limit := internalweb.AuditDefaultLimit
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 9999 {
			limit = parsed
		}
	}
	rows, err := ws.Audit.TailAudit(limit)
	if err != nil {
		http.Error(w, "central web: tail audit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := centralAuditData{
		centralPageData: s.pageData(orgID, wsID, "audit"),
		Rows:            rows,
		Limit:           limit,
		Source:          "the central event store",
	}
	s.renderer.Render(w, r, "audit.tmpl", data)
}
