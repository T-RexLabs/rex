package web

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed templates/*.tmpl templates/pages/*.tmpl
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

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
}

// Server is the local web UI handler. Routes register on its mux;
// templates and static assets come from the embed.FS.
type Server struct {
	opts        Options
	mux         *http.ServeMux
	pages       map[string]*template.Template
	highlighter *Highlighter
}

// New constructs a Server, parses templates from the embed.FS, and
// registers routes.
func New(opts Options) (*Server, error) {
	if opts.WorkspaceRoot == "" {
		return nil, errors.New("web: WorkspaceRoot is required")
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.BindAddr == "" {
		opts.BindAddr = "(unspecified)"
	}

	pages, err := loadPages()
	if err != nil {
		return nil, err
	}

	s := &Server{
		opts:        opts,
		mux:         http.NewServeMux(),
		pages:       pages,
		highlighter: newHighlighter(),
	}
	s.registerRoutes()
	return s, nil
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) registerRoutes() {
	staticSub, _ := fs.Sub(staticFS, "static")
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
	s.mux.HandleFunc("GET /runs", s.handleRunsList)
	s.mux.HandleFunc("GET /runs/new", s.handleRunNew)
	s.mux.HandleFunc("POST /runs/start", s.handleRunStart)
	s.mux.HandleFunc("GET /runs/{id}", s.handleRunDetail)
	s.mux.HandleFunc("GET /runs/{id}/stream", s.handleRunStream)
	s.mux.HandleFunc("GET /audit", s.handleAudit)
	s.mux.HandleFunc("GET /remotes", s.handleRemotes)
	s.mux.HandleFunc("GET /search", s.handleSearch)
	s.mux.HandleFunc("GET /settings", s.handleSettings)
	s.mux.HandleFunc("GET /sync", s.handleSyncPage)
	s.mux.HandleFunc("POST /sync", s.handleSyncRun)
}

// loadPages reads base.tmpl + every templates/pages/*.tmpl and
// returns a map keyed by page basename ("home.tmpl" → composed
// template). Each entry parses base.tmpl + that page in one
// ParseFS call so html/template's contextual escaper analyzes the
// combined tree once. Splitting it into two Parse calls leaves the
// title-template escaper in an inconsistent state when later pages
// reference different value paths in {{define "title"}}.
func loadPages() (map[string]*template.Template, error) {
	pages, err := fs.Glob(templateFS, "templates/pages/*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("web: glob pages: %w", err)
	}
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		name := filepath.Base(p)
		t, err := template.New(name).ParseFS(templateFS, "templates/base.tmpl", p)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", p, err)
		}
		out[name] = t
	}
	return out, nil
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
}

func (s *Server) basePageData() pageData {
	ws, _ := loadWorkspaceSummary(s.opts.WorkspaceRoot)
	return pageData{
		Workspace: ws,
		BindAddr:  s.opts.BindAddr,
		Version:   s.opts.Version,
	}
}

// withNav returns a copy of base with NavSection set. Tiny helper
// so handlers don't repeat the assignment dance.
func (p pageData) withNav(s string) pageData {
	p.NavSection = s
	return p
}

// render executes base+page against data. Returns a 500 on
// template errors so the user sees something rather than a blank
// response.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	tmpl, ok := s.pages[page]
	if !ok {
		http.Error(w, "web: unknown page "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		// Best-effort: the response may already be partially
		// written, so we can't reliably switch status codes
		// here. The error is logged at the cmd/rex layer via
		// the http.Server's ErrorLog.
		_, _ = fmt.Fprintf(w, "\n<!-- web: render error: %s -->\n", strings.ReplaceAll(err.Error(), "-->", "—>"))
	}
}
