package web

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// specEditData backs spec_edit.tmpl. Body is the textarea's
// current value; on the GET path it's the file contents, on a
// rerender after a failed POST it's the user's last submission.
type specEditData struct {
	pageData
	SpecID string
	Body   string
	Error  string
}

// handleSpecEdit renders GET /specs/<id>/edit. The form is a
// single textarea with the spec's YAML body and a save button.
// We don't try to syntax-highlight the textarea — chroma can't
// style native textareas, and shipping CodeMirror/Monaco would
// blow the no-build-step / loopback-only constraints. Mono font
// + tab handling is enough for a workspace-local editor.
func (s *Server) handleSpecEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || !specfmt.IsKebab(id) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.opts.WorkspaceRoot, ".rex", "specs", id+".yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "web: read spec: "+err.Error(), http.StatusInternalServerError)
		return
	}
	base := s.basePageData()
	base.NavSection = "specs"
	s.render(w, r, "spec_edit.tmpl", specEditData{
		pageData: base,
		SpecID:   id,
		Body:     string(body),
	})
}

// handleSpecSave handles POST /specs/<id>/edit. On a clean save
// it 303-redirects to /specs/<id>; on any failure (parse, id
// mismatch, strict-validation error) it rerenders the form with
// the user's draft preserved and an error banner.
//
// Strict validation runs against a Workspace containing every
// other spec in the workspace plus this one (so ACID references
// can resolve). That mirrors `rex spec validate specs/*.yaml`
// applied to the would-be on-disk state — errors here are
// exactly the ones the user would see if they wrote the file
// manually and ran the CLI validator.
//
// We refuse to silently rename: if metadata.id in the body no
// longer matches the URL path's id, we reject. Renames belong
// in a dedicated CLI flow (or a future POST /specs/<id>/rename).
func (s *Server) handleSpecSave(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || !specfmt.IsKebab(id) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	body := r.FormValue("body")

	rerender := func(msg string) {
		base := s.basePageData()
		base.NavSection = "specs"
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "spec_edit.tmpl", specEditData{
			pageData: base,
			SpecID:   id,
			Body:     body,
			Error:    msg,
		})
	}

	doc, err := specfmt.Parse(strings.NewReader(body))
	if err != nil {
		rerender("YAML parse failed: " + err.Error())
		return
	}
	if doc.Metadata.ID != id {
		rerender(fmt.Sprintf(
			"metadata.id is %q but you're editing %q. Renames need a dedicated flow; reset metadata.id or recreate the spec.",
			doc.Metadata.ID, id))
		return
	}

	// Run strict validation against a workspace built from every
	// OTHER spec on disk plus the proposed body. That way ACID
	// references in `body` resolve against the full registry.
	ws := specfmt.NewWorkspace()
	dir := filepath.Join(s.opts.WorkspaceRoot, ".rex", "specs")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if e.Name() == id+".yaml" {
			continue // replaced by the edited body below
		}
		other, perr := specfmt.ParseFile(filepath.Join(dir, e.Name()))
		if perr != nil {
			continue // ignore broken peers; the validator will flag those when the user runs validate-all
		}
		_ = ws.Add(other)
	}
	doc.Path = filepath.Join(dir, id+".yaml")
	if err := ws.Add(doc); err != nil {
		rerender("workspace add: " + err.Error())
		return
	}
	res := specfmt.ValidateWorkspace(ws, specfmt.ModeStrict)
	if res.HasErrors() {
		rerender(formatValidationErrors(res.Issues, doc.Path))
		return
	}

	if err := os.WriteFile(doc.Path, []byte(body), 0o644); err != nil {
		rerender("write " + doc.Path + ": " + err.Error())
		return
	}
	http.Redirect(w, r, "/specs/"+id, http.StatusSeeOther)
}

// formatValidationErrors squashes the strict-validate result into
// a multi-line message suitable for the error banner. Only
// errors are surfaced; warnings would create false alarms on
// save (the user might be intentionally ignoring them).
func formatValidationErrors(issues []specfmt.Issue, ownPath string) string {
	var lines []string
	for _, iss := range issues {
		if iss.Severity != specfmt.SeverityError {
			continue
		}
		// Only show errors from the file we're saving — the
		// user can't fix issues in other specs from this form.
		if iss.File != "" && iss.File != ownPath {
			continue
		}
		prefix := iss.Path
		if prefix == "" {
			prefix = iss.Category
		}
		lines = append(lines, prefix+": "+iss.Message)
	}
	if len(lines) == 0 {
		return "validation failed (no per-issue messages)"
	}
	return "validation: " + strings.Join(lines, "; ")
}
