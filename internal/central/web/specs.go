package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/sync/proto"
	internalweb "github.com/asabla/rex/internal/web"
)

// GitEntityReader is the subset of the central GitStore the
// central spec projection needs. Defined here so the web shell
// doesn't depend on internal/central/server directly — the
// cmd-level wireup binds the central server's GitStore to this
// interface. Mirrors the read-side methods of server.GitStore
// (sync.API.4); the web side never writes.
type GitEntityReader interface {
	Get(ctx context.Context, path string) (proto.GitEntity, error)
	List(ctx context.Context) ([]string, error)
}

// centralSpecProjection satisfies internalweb.SpecProjection
// against a central GitEntityReader. Specs live at `specs/<id>.yaml`
// in the store (sync.CAT.2); proposed amendments under
// `specs/_proposed/...` are filtered out here so they don't
// appear on the /specs page (they belong to the amendments
// sub-task).
//
// v1 limitation: the central GitStore is currently single-workspace
// (no workspace_id scoping). The resolver returns the same
// projection for every workspace id until that refactor lands; the
// /orgs/<org-id>/workspaces/<ws-id>/ URL shape is forward-compatible
// but doesn't yet enforce isolation between workspaces on the
// data-store side.
type centralSpecProjection struct {
	store GitEntityReader
	ctx   context.Context
}

func newCentralSpecProjection(ctx context.Context, store GitEntityReader) centralSpecProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralSpecProjection{store: store, ctx: ctx}
}

func (p centralSpecProjection) ListSpecs() ([]internalweb.SpecRow, error) {
	if p.store == nil {
		return nil, nil
	}
	paths, err := p.store.List(p.ctx)
	if err != nil {
		return nil, fmt.Errorf("central specs: list: %w", err)
	}
	out := make([]internalweb.SpecRow, 0, len(paths))
	for _, path := range paths {
		if !isSpecPath(path) {
			continue
		}
		rec, err := p.store.Get(p.ctx, path)
		if err != nil {
			// Single missing entity shouldn't 500 the list.
			continue
		}
		doc, err := specfmt.Parse(strings.NewReader(rec.Content))
		if err != nil {
			continue
		}
		out = append(out, internalweb.SpecRow{
			ID:             doc.Metadata.ID,
			Name:           doc.Metadata.Name,
			State:          doc.Metadata.State,
			TaskCount:      len(doc.Tasks),
			ComponentCount: len(doc.Components),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p centralSpecProjection) OpenSpec(id string) (*specfmt.Document, string, bool, error) {
	if p.store == nil {
		return nil, "", false, nil
	}
	rec, err := p.store.Get(p.ctx, "specs/"+id+".yaml")
	if err != nil {
		// The server package owns ErrUnknownGitEntity; rather than
		// import-coupling, treat any Get error as "not present"
		// for the central spec read path. The store surfaces
		// transient errors via a wrapped error message a future
		// projection layer can pattern-match if needed.
		if errors.Is(err, errUnknownEntity) {
			return nil, "", false, nil
		}
		// Best-effort: missing entity ≈ not found for the web UI.
		// Errors here are extremely rare (memory store has no
		// fallible reads); we tolerate them as 404 to keep the
		// page robust.
		return nil, "", false, nil
	}
	doc, err := specfmt.Parse(strings.NewReader(rec.Content))
	if err != nil {
		return nil, "", false, fmt.Errorf("central specs: parse %s: %w", id, err)
	}
	return doc, rec.Content, true, nil
}

// errUnknownEntity matches the server package's
// ErrUnknownGitEntity by error message rather than type, to keep
// internal/central/web from importing internal/central/server.
// Reserved for future use; today the projection treats any Get
// error as not-found.
var errUnknownEntity = errors.New("unknown git entity")

// isSpecPath reports whether path under the GitStore names a spec
// file directly under `specs/`. Excludes `specs/_proposed/...` and
// any nested subdirectories so the /specs list shows only the
// canonical spec set.
func isSpecPath(path string) bool {
	if !strings.HasPrefix(path, "specs/") || !strings.HasSuffix(path, ".yaml") {
		return false
	}
	rest := strings.TrimPrefix(path, "specs/")
	if strings.Contains(rest, "/") {
		// nested (e.g. specs/_proposed/foo.yaml) — skip.
		return false
	}
	return true
}

// centralWorkspaceResolver is the central shell's
// WorkspaceResolver: it ignores the workspaceID input in v1
// (single-workspace GitStore limitation) and always returns the
// one workspace backed by the bound GitStore. When the
// multi-workspace GitStore refactor lands, this resolver grows
// per-workspace dispatch without changing the handler call sites.
type centralWorkspaceResolver struct {
	store GitEntityReader
}

// NewGitStoreResolver builds an internalweb.WorkspaceResolver
// backed by a GitEntityReader (typically the central node's
// GitStore). v1 single-workspace limitation per
// centralWorkspaceResolver's doc. Exported so cmd/rex-central can
// wire the resolver without depending on the private constructor.
func NewGitStoreResolver(store GitEntityReader) internalweb.WorkspaceResolver {
	return newCentralWorkspaceResolver(store)
}

func newCentralWorkspaceResolver(store GitEntityReader) centralWorkspaceResolver {
	return centralWorkspaceResolver{store: store}
}

func (r centralWorkspaceResolver) Resolve(workspaceID string) (internalweb.Workspace, error) {
	return internalweb.Workspace{
		ID:    workspaceID,
		Specs: newCentralSpecProjection(context.Background(), r.store),
	}, nil
}

// centralPageData mirrors the field shape the shared templates
// expect (base.tmpl reads .Workspace.ID, .BindAddr, .Version,
// .NavSection, .SearchScope). Each shell defines its own
// page-data envelope; the central one populates Workspace from
// the resolver and leaves SearchScope empty (the org/workspace-
// scoped picker lands with central-read-side-search-amendments).
type centralPageData struct {
	Workspace   *centralWorkspaceSummary
	BindAddr    string
	Version     string
	NavSection  string
	SearchScope internalweb.ScopePickerData
	// OrgID + WorkspaceID surface in URLs the templates link to
	// (e.g. /orgs/<org-id>/workspaces/<ws-id>/specs/<spec-id>).
	// Local templates ignore these fields when absent; the
	// per-route partials that need them are introduced as the
	// central read-side handlers land.
	OrgID       string
	WorkspaceID string
}

// centralWorkspaceSummary is the shape base.tmpl's .Workspace
// branch expects (just .ID is used today). The central side
// derives it from the workspace id alone in v1; richer fields
// land when the workspaces table is read for the index page.
type centralWorkspaceSummary struct {
	ID string
}

// centralSpecsListData is the central-side envelope for
// specs_list.tmpl. Embeds centralPageData so base.tmpl finds the
// fields it expects, then carries the shared []SpecRow.
type centralSpecsListData struct {
	centralPageData
	Specs []internalweb.SpecRow
}

// centralSpecDetailData is the central-side envelope for
// spec_detail.tmpl. Embeds the shared SpecContent so .Spec,
// .RawYAML, .YAMLPretty resolve in the template; the
// shell-specific extras (Amendments, RunsByTask, Harnesses,
// UntaskedRuns, AllRuns, RunCount) are present-as-zero so
// `{{if .Amendments}}` style guards in the template short-circuit
// cleanly. Each will be populated in v1 as its own sub-task lifts
// the corresponding projection (Amendments →
// central-read-side-search-amendments; runs → central-read-side-runs-audit;
// Harnesses requires central-side execution and remains empty in v1).
type centralSpecDetailData struct {
	centralPageData
	internalweb.SpecContent
	ActiveTab    string
	Amendments   []emptyRow
	Harnesses    []emptyRow
	RunsByTask   map[string][]emptyRow
	UntaskedRuns []emptyRow
	AllRuns      []emptyRow
	RunCount     int
}

// emptyRow is a placeholder for fields the central data envelope
// must expose for template compatibility but does not populate in
// v1. The shared spec_detail.tmpl uses `{{if .X}}` / `{{range .X}}`
// guards — both fire false / iterate zero times against a nil
// slice — so the page renders cleanly with these all empty.
//
// Replaced by the real row types as each lift lands. The dedicated
// type (rather than `any`) keeps the field signatures
// self-documenting and the eventual replacement a localised diff.
type emptyRow struct{}

// handleSpecsList is GET /orgs/<org-id>/workspaces/<ws-id>/specs.
// Pulls the list from the resolver's SpecProjection and renders
// specs_list.tmpl with the central page envelope.
func (s *Server) handleSpecsList(w http.ResponseWriter, r *http.Request) {
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
	if ws.Specs == nil {
		http.Error(w, "central web: workspace has no spec projection", http.StatusServiceUnavailable)
		return
	}
	rows, err := ws.Specs.ListSpecs()
	if err != nil {
		http.Error(w, "central web: list specs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := centralSpecsListData{
		centralPageData: s.pageData(orgID, wsID, "specs"),
		Specs:           rows,
	}
	s.renderer.Render(w, r, "specs_list.tmpl", data)
}

// handleSpecDetail is GET /orgs/<org-id>/workspaces/<ws-id>/specs/<spec-id>.
// Calls the shared LoadSpecContent against the workspace's
// SpecProjection and renders spec_detail.tmpl with the central
// envelope. 404 for unknown spec ids; 400 for non-kebab ids.
func (s *Server) handleSpecDetail(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	wsID := r.PathValue("ws")
	specID := r.PathValue("id")
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "rendered"
	}
	switch tab {
	case "rendered", "source", "tasks", "runs":
	default:
		tab = "rendered"
	}

	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	if ws.Specs == nil {
		http.Error(w, "central web: workspace has no spec projection", http.StatusServiceUnavailable)
		return
	}
	content, found, err := internalweb.LoadSpecContent(ws.Specs, specID, s.highlighter)
	if err != nil {
		http.Error(w, "central web: load spec: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	data := centralSpecDetailData{
		centralPageData: s.pageData(orgID, wsID, "specs"),
		SpecContent:     content,
		ActiveTab:       tab,
	}
	s.renderer.Render(w, r, "spec_detail.tmpl", data)
}

// pageData builds the per-request page envelope. Mirrors the
// local shell's basePageData but populated from URL segments
// (resolver provides workspace identity; auth/session will provide
// the org identity when the gate lands).
func (s *Server) pageData(orgID, wsID, navSection string) centralPageData {
	return centralPageData{
		Workspace:   &centralWorkspaceSummary{ID: wsID},
		BindAddr:    s.opts.BindAddr,
		Version:     s.opts.Version,
		NavSection:  navSection,
		OrgID:       orgID,
		WorkspaceID: wsID,
	}
}
