package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/specfmt"
)

// specNewData backs the spec_new.tmpl form. Templates is the
// list of template specs available in the workspace; the form
// renders an "(minimal skeleton)" option that maps to the empty
// string so the handler can resolve it back.
type specNewData struct {
	pageData
	Error    string
	ID       string
	Name     string
	State    string
	Template string // selected template id, or "" for minimal skeleton
	Force    bool

	States          []string
	Templates       []specTemplateOption
	DefaultTemplate string
}

// availableStates is the canonical list of metadata.state values
// the form offers. Mirrors the values the CLI accepts; the
// validator does not constrain state to this list (it accepts any
// non-empty string), so this is a UX hint, not a hard rule.
var availableStates = []string{"draft", "active", "stable", "deprecated"}

type specTemplateOption struct {
	ID        string
	Name      string
	IsDefault bool
}

// slugifyForSpecID converts a free-form display name into a
// kebab-case spec id: lowercase ASCII letters + digits joined by
// single hyphens, no leading/trailing hyphens, runs of
// non-alphanumerics collapsed to one hyphen. Returns empty when
// the input has no usable characters (e.g. only punctuation).
//
// "My Spec Name"      → "my-spec-name"
// "audit & sync v2"   → "audit-sync-v2"
// "  --weird---name " → "weird-name"
// "🔥🔥🔥"            → "" (non-ASCII drops)
func slugifyForSpecID(name string) string {
	var b strings.Builder
	prevHyphen := true // suppress leading hyphens
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := b.String()
	out = strings.TrimSuffix(out, "-")
	return out
}

// handleSpecNew renders GET /specs/new — the create form.
func (s *Server) handleSpecNew(w http.ResponseWriter, r *http.Request) {
	d := s.loadSpecNewData("", "", "draft", "")
	d.NavSection = "specs"
	s.render(w, r, "spec_new.tmpl", d)
}

// handleSpecCreate handles POST /specs/create. On success it
// 303-redirects to /specs/<id>; on failure it rerenders the form
// with an error banner so the user keeps their input.
func (s *Server) handleSpecCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	state := strings.TrimSpace(r.FormValue("state"))
	template := strings.TrimSpace(r.FormValue("template"))
	force := r.FormValue("force") == "on"
	if state == "" {
		state = "draft"
	}

	rerender := func(msg string) {
		d := s.loadSpecNewData(id, name, state, template)
		d.NavSection = "specs"
		d.Error = msg
		d.Force = force
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "spec_new.tmpl", d)
	}

	// When the user leaves id blank, derive it from name —
	// "My Spec Name" → "my-spec-name". Saves a step on the
	// happy path while preserving the option to type a custom
	// id (e.g. when name has punctuation that doesn't sluggify
	// cleanly).
	if id == "" {
		id = slugifyForSpecID(name)
		if id == "" {
			rerender("spec id is required (or supply a name we can derive it from)")
			return
		}
	}
	if !specfmt.IsKebab(id) {
		rerender(fmt.Sprintf("spec id %q is not kebab-case", id))
		return
	}

	root := s.opts.WorkspaceRoot
	if root == "" {
		http.Error(w, "web: no workspace root configured", http.StatusInternalServerError)
		return
	}

	tplDoc, err := s.resolveTemplate(root, template)
	if err != nil {
		rerender(err.Error())
		return
	}

	scaffoldOpts := specfmt.ScaffoldOptions{
		ID:       id,
		Name:     name,
		State:    state,
		Template: tplDoc,
	}

	path := filepath.Join(root, ".rex", "specs", id+".yaml")
	if !force {
		if _, err := os.Stat(path); err == nil {
			rerender(fmt.Sprintf("%s.yaml already exists. Tick 'overwrite existing' to replace it.", id))
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		rerender("create specs dir: " + err.Error())
		return
	}

	// Mirror the CLI's two emit paths: typed Document +
	// yaml.Marshal when a template applies, hand-rolled
	// MinimalSkeletonYAML otherwise so authors get the rich
	// commented placeholder body rather than a bare metadata
	// block.
	var body []byte
	if tplDoc != nil {
		doc, derr := specfmt.NewSpecFromTemplate(scaffoldOpts)
		if derr != nil {
			rerender(derr.Error())
			return
		}
		body, err = yaml.Marshal(doc)
		if err != nil {
			rerender("marshal spec: " + err.Error())
			return
		}
	} else {
		body, err = specfmt.MinimalSkeletonYAML(scaffoldOpts)
		if err != nil {
			rerender(err.Error())
			return
		}
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		rerender("write " + path + ": " + err.Error())
		return
	}

	http.Redirect(w, r, "/specs/"+id, http.StatusSeeOther)
}

// loadSpecNewData scans the workspace for templates and builds
// the form's render data. id/name/state/template are the values
// to redisplay (empty on first GET; populated on rerender after
// validation failure).
func (s *Server) loadSpecNewData(id, name, state, template string) specNewData {
	base := s.basePageData()
	d := specNewData{
		pageData: base,
		ID:       id,
		Name:     name,
		State:    state,
		Template: template,
		States:   availableStates,
	}
	if d.State == "" {
		d.State = "draft"
	}

	root := s.opts.WorkspaceRoot
	if root == "" {
		return d
	}

	// Discover available templates by parsing every spec in
	// .rex/specs and keeping the ones with extra.template == true.
	dir := filepath.Join(root, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return d
	}
	def := readDefaultTemplateID(root)
	d.DefaultTemplate = def

	var tpls []specTemplateOption
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		doc, err := specfmt.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if doc.Extra == nil {
			continue
		}
		raw, ok := doc.Extra["template"].(bool)
		if !ok || !raw {
			continue
		}
		tpls = append(tpls, specTemplateOption{
			ID:        doc.Metadata.ID,
			Name:      doc.Metadata.Name,
			IsDefault: doc.Metadata.ID == def,
		})
	}
	sort.Slice(tpls, func(i, j int) bool { return tpls[i].ID < tpls[j].ID })
	d.Templates = tpls
	return d
}

// resolveTemplate mirrors the CLI's resolveTemplateForCreate but
// inlined here to keep the web package self-contained. Empty
// id selects "minimal skeleton" (returns nil).
func (s *Server) resolveTemplate(root, id string) (*specfmt.Document, error) {
	if id == "" {
		return nil, nil
	}
	paths, err := listSpecFilesIn(filepath.Join(root, ".rex", "specs"))
	if err != nil {
		return nil, err
	}
	ws := specfmt.NewWorkspace()
	for _, p := range paths {
		doc, err := specfmt.ParseFile(p)
		if err != nil {
			continue
		}
		_ = ws.Add(doc)
	}
	t := ws.Template(id)
	if t == nil {
		return nil, fmt.Errorf("template %q not found in workspace (or extra.template != true)", id)
	}
	return t, nil
}

// listSpecFilesIn returns absolute *.yaml paths in dir
// (non-recursive). Empty slice when dir is missing.
func listSpecFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

// readDefaultTemplateID parses workspace.yaml's
// extra.default_template_id when present. Best effort.
func readDefaultTemplateID(root string) string {
	body, err := os.ReadFile(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil {
		return ""
	}
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return ""
	}
	extra, ok := raw["extra"].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := extra[specfmt.ExtraDefaultTemplateID].(string)
	return v
}
