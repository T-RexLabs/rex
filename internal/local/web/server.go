package web

import (
	"context"
	"errors"
	"io/fs"
	"net/http"

	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/local/remotes"
	internalweb "github.com/asabla/rex/internal/web"
)

// Options configure New.
type Options struct {
	// WorkspaceRoot is the absolute path to the workspace this
	// server serves. Required: the v1 server is rooted in one
	// workspace.
	WorkspaceRoot string
	// BindAddr is the listening address; surfaced in the footer
	// so users see what the binary is bound to. Required for the
	// template only — actual binding happens in cmd/rex serve.
	BindAddr string
	// Version surfaces in the footer for diagnosis.
	Version string
	// Context is the server-lifetime context background tasks should
	// inherit (e.g. async runs started from the web UI). Nil means
	// context.Background().
	Context context.Context
	// Adapters overrides the harness registry the run-start form uses.
	// Nil means adapter.Default(), which is what the production CLI
	// wiring wants; tests inject a custom registry.
	Adapters *adapter.Registry
}

// Server is the local web UI handler. Routes register on its mux;
// shared templates / static assets / render machinery come from
// internal/web (web-ui.CENTRAL-LAYOUT.1); workspace-bound state
// (handlers, file paths, harness cache) lives here in the local
// shell.
type Server struct {
	opts         Options
	ctx          context.Context
	harnesses    *harnessCache
	ownedRuns    *ownedRuns
	interactions *runInteractionHub
	mux          *http.ServeMux
	renderer     *internalweb.Renderer
	highlighter  *internalweb.Highlighter
	resolver     internalweb.WorkspaceResolver
}

// New constructs a Server, parses templates from the shared
// internal/web package, and registers routes.
func New(opts Options) (*Server, error) {
	warmHarnesses := opts.Context != nil
	if opts.WorkspaceRoot == "" {
		return nil, errors.New("web: WorkspaceRoot is required")
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.BindAddr == "" {
		opts.BindAddr = "(unspecified)"
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}

	renderer, err := internalweb.NewRenderer()
	if err != nil {
		return nil, err
	}

	s := &Server{
		opts:         opts,
		ctx:          opts.Context,
		harnesses:    newHarnessCache(opts.Context, opts.Adapters, opts.WorkspaceRoot, warmHarnesses),
		ownedRuns:    &ownedRuns{},
		interactions: newRunInteractionHub(),
		mux:          http.NewServeMux(),
		renderer:     renderer,
		highlighter:  internalweb.NewHighlighter(),
		resolver:     localResolver{root: opts.WorkspaceRoot},
	}
	s.registerRoutes()
	return s, nil
}

// localResolver is the local shell's single-workspace WorkspaceResolver.
// It ignores the requested workspaceID and always returns the workspace
// rooted at the bound path (web-ui.LOCAL.1.1, CENTRAL-LAYOUT.3). The
// workspace's ID is read lazily from .rex/workspace.yaml; failures
// return the empty-id workspace so the binding still works for
// fresh-init workspaces.
type localResolver struct{ root string }

func (l localResolver) Resolve(string) (internalweb.Workspace, error) {
	ws := internalweb.Workspace{Root: l.root}
	if summary, err := loadWorkspaceSummary(l.root); err == nil && summary != nil {
		ws.ID = summary.ID
	}
	return ws, nil
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Shutdown waits for background runs owned by this rex serve process to
// finish observing cancellation and append their terminal events.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.ownedRuns == nil {
		return nil
	}
	return s.ownedRuns.wait(ctx)
}

func (s *Server) registerRoutes() {
	staticSub, _ := fs.Sub(internalweb.StaticFS, "static")
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	s.mux.HandleFunc("GET /static/chroma.css", s.handleChromaCSS)
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /specs", s.handleSpecsList)
	s.mux.HandleFunc("GET /specs/new", s.handleSpecNew)
	s.mux.HandleFunc("POST /specs/create", s.handleSpecCreate)
	s.mux.HandleFunc("POST /specs/validate", s.handleSpecsValidate)
	s.mux.HandleFunc("GET /specs/{id}", s.handleSpecDetail)
	s.mux.HandleFunc("GET /specs/{id}/edit", s.handleSpecEdit)
	s.mux.HandleFunc("POST /specs/{id}/edit", s.handleSpecSave)
	s.mux.HandleFunc("POST /specs/{id}/tasks/{taskID}/run", s.handleSpecTaskRun)
	s.mux.HandleFunc("POST /specs/{id}/ask", s.handleSpecAdHocAction)
	s.mux.HandleFunc("GET /runs", s.handleRunsList)
	s.mux.HandleFunc("GET /runs/new", s.handleRunNew)
	s.mux.HandleFunc("POST /runs/start", s.handleRunStart)
	s.mux.HandleFunc("POST /runs/{id}/input", s.handleRunInput)
	s.mux.HandleFunc("GET /runs/{id}", s.handleRunDetail)
	s.mux.HandleFunc("GET /runs/{id}/stream", s.handleRunStream)
	s.mux.HandleFunc("POST /runs/{id}/permission", s.handleRunPermission)
	s.mux.HandleFunc("GET /amendments", s.handleAmendmentsList)
	s.mux.HandleFunc("GET /amendments/{stem}", s.handleAmendmentDetail)
	s.mux.HandleFunc("POST /amendments/{stem}/accept", s.handleAmendmentAccept)
	s.mux.HandleFunc("POST /amendments/{stem}/reject", s.handleAmendmentReject)
	s.mux.HandleFunc("GET /audit", s.handleAudit)
	s.mux.HandleFunc("GET /remotes", s.handleRemotes)
	s.mux.HandleFunc("GET /search", s.handleSearch)
	s.mux.HandleFunc("POST /search", s.handleSearch)
	s.mux.HandleFunc("GET /settings", s.handleSettings)
	s.mux.HandleFunc("GET /sync", s.handleSyncPage)
	s.mux.HandleFunc("POST /sync", s.handleSyncRun)
}

// pageData is what every page receives via the base template's
// {{.Workspace}} / {{.Version}} / {{.BindAddr}} / {{.NavSection}}
// fields. Pages embed it via composition so they can add their
// own fields without reflecting through map[string]any.
//
// NavSection drives the active-link state in the top nav. One of:
// "home", "specs", "runs", "audit", "remotes". Empty for pages
// that don't fit any (e.g. /specs/new still highlights "specs").
type pageData struct {
	Workspace  *workspaceSummary
	BindAddr   string
	Version    string
	NavSection string
	// SearchScope drives the topbar scope picker. Empty Selected
	// means "current workspace". Remotes is the list of registered
	// remotes the user can dispatch a cross-workspace search to.
	SearchScope internalweb.ScopePickerData
	// CentralOnly is always false on the local shell; declared
	// so the shared base.tmpl's banner branch can short-circuit
	// uniformly on either shell (web-ui.CENTRAL.2).
	CentralOnly bool
}

// ScopeOption / ScopePickerData live in internal/web (the
// scope_picker partial is shared across both shells). Re-exported
// here as type aliases so existing local handler code can stay on
// the old names without churn.
type (
	ScopeOption     = internalweb.ScopeOption
	ScopePickerData = internalweb.ScopePickerData
)

func (s *Server) basePageData() pageData {
	ws, _ := loadWorkspaceSummary(s.opts.WorkspaceRoot)
	return pageData{
		Workspace: ws,
		BindAddr:  s.opts.BindAddr,
		Version:   s.opts.Version,
		SearchScope: ScopePickerData{
			Remotes: loadScopeRemotes(),
		},
	}
}

// newPageDataFromOpts constructs the same pageData that basePageData
// produces but only takes Options — for the read-heavy pages whose
// data loaders construct their own pageData rather than going through
// the Server receiver. The scope picker's Remotes list is populated
// best-effort; an unreadable registry yields an empty picker (current
// workspace only) instead of a 500.
func newPageDataFromOpts(opts Options) pageData {
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	return pageData{
		Workspace: ws,
		BindAddr:  opts.BindAddr,
		Version:   opts.Version,
		SearchScope: ScopePickerData{
			Remotes: loadScopeRemotes(),
		},
	}
}

// loadScopeRemotes returns the registered remotes as scope options,
// best-effort. The picker is purely a UI affordance; failures here
// fall through to "no remote choices" rather than failing the page.
func loadScopeRemotes() []ScopeOption {
	regPath, err := remotes.DefaultPath()
	if err != nil || regPath == "" {
		return nil
	}
	reg, err := remotes.Load(regPath)
	if err != nil {
		return nil
	}
	out := make([]ScopeOption, 0, 4)
	for _, r := range reg.List() {
		out = append(out, ScopeOption{Value: "remote:" + r.Name, Label: r.Name})
	}
	return out
}

// render is a thin wrapper over the shared renderer's Render.
// Callers go through it so existing handler code continues to call
// s.render(...) without churn; the actual template execution lives
// in internal/web.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.renderer.Render(w, r, page, data)
}
