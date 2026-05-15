package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

// Renderer owns the parsed template tree and the small FuncMap both
// binaries' pages share. One Renderer is built per process at
// startup and reused across requests; it is safe for concurrent use
// (html/template is read-only after parse).
type Renderer struct {
	pages map[string]*template.Template
}

// NewRenderer parses base.tmpl + every templates/partials/*.tmpl +
// every templates/pages/*.tmpl from TemplateFS and returns a
// Renderer keyed by page basename ("home.tmpl" → composed
// template). Each entry parses base + partials + that page in one
// ParseFS call so html/template's contextual escaper analyzes the
// combined tree once. Splitting into multiple Parse calls leaves
// the title-template escaper in an inconsistent state when later
// pages reference different value paths in {{define "title"}}.
//
// Partials are the shared component library (web-ui.SHARED.1).
// Every page sees them all so callers don't have to enumerate
// which partials they need.
func NewRenderer() (*Renderer, error) {
	pages, err := fs.Glob(TemplateFS, "templates/pages/*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("web: glob pages: %w", err)
	}
	partials, err := fs.Glob(TemplateFS, "templates/partials/*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("web: glob partials: %w", err)
	}
	parseTargets := append([]string{"templates/base.tmpl"}, partials...)
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		name := filepath.Base(p)
		t, err := template.New(name).Funcs(funcMap()).ParseFS(TemplateFS, append(parseTargets, p)...)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", p, err)
		}
		out[name] = t
	}
	return &Renderer{pages: out}, nil
}

// Render executes the named page (composed with base + partials)
// against data. The "base" template is the entry point; pages
// override its blocks. Render returns a 500 when the page name is
// unknown; mid-stream template errors are appended as an HTML
// comment because the response status has already been written.
func (r *Renderer) Render(w http.ResponseWriter, _ *http.Request, page string, data any) {
	tmpl, ok := r.pages[page]
	if !ok {
		http.Error(w, "web: unknown page "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		_, _ = fmt.Fprintf(w, "\n<!-- web: render error: %s -->\n", strings.ReplaceAll(err.Error(), "-->", "—>"))
	}
}

// HasPage reports whether the named page is loaded. Tests use this
// to assert the embed FS contains what they expect; production code
// rarely needs it.
func (r *Renderer) HasPage(name string) bool {
	_, ok := r.pages[name]
	return ok
}

// Page returns the parsed template for the named page (composed
// with base + partials), or nil when the page is unknown. Tests use
// this to drive partials directly without spinning up a full
// server; production handlers should call Render instead.
func (r *Renderer) Page(name string) *template.Template {
	return r.pages[name]
}

// funcMap is the small set of template helpers shared by every
// page. Add new helpers here only when they are template-shape
// utilities; data shaping belongs in the page's data loader.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"joinCSV": func(xs []string) string { return strings.Join(xs, ",") },
		// splitTaskRef cracks `<spec-id>.<task-id>` into its two
		// parts so the template can build a /specs/<id> link with a
		// #<task-id> anchor. Returns the original string in slot 0
		// if it doesn't match the expected shape so the caller can
		// fall back to a plain code span.
		"splitTaskRef": func(s string) []string {
			idx := strings.Index(s, ".")
			if idx < 0 {
				return []string{s}
			}
			return []string{s[:idx], s[idx+1:]}
		},
		// splitACID returns the spec id portion of an ACID
		// (everything before the first dot). Used to link a run's
		// spec_refs back to /specs/<spec-id>.
		"splitACID": func(s string) []string {
			idx := strings.Index(s, ".")
			if idx < 0 {
				return nil
			}
			return []string{s[:idx]}
		},
	}
}
