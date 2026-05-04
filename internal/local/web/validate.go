package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// validateData backs validate.tmpl: the result of POST
// /specs/validate.
type validateData struct {
	pageData
	SpecCount int
	Errors    int
	Warnings  int
	Issues    []validateIssue
}

// validateIssue is one renderable issue from the validator.
// Categories and severities mirror specfmt.Issue but without the
// internal types so the template stays narrow.
type validateIssue struct {
	File     string
	Path     string
	Category string
	Message  string
	Severity string
}

// handleSpecsValidate runs the strict validator across every spec
// in .rex/specs and renders the result inline. It is a POST so
// browsers don't repeatedly run the validator on a refresh — the
// canonical "no spec drift" gate is `rex spec validate` from the
// CLI; this surface mirrors it for users who never leave the
// browser. POST/redirect/GET would lose the result, so we render
// inline (status 200 with full output).
func (s *Server) handleSpecsValidate(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(s.opts.WorkspaceRoot, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "web: read specs dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)

	ws := specfmt.NewWorkspace()
	var issues []specfmt.Issue
	for _, p := range paths {
		doc, err := specfmt.ParseFile(p)
		if err != nil {
			issues = append(issues, specfmt.Issue{
				File:     p,
				Category: "parse",
				Severity: specfmt.SeverityError,
				Message:  err.Error(),
			})
			continue
		}
		if err := ws.Add(doc); err != nil {
			issues = append(issues, specfmt.Issue{
				File:     p,
				Category: "parse",
				Severity: specfmt.SeverityError,
				Message:  err.Error(),
			})
		}
	}
	res := specfmt.ValidateWorkspace(ws, specfmt.ModeStrict)
	issues = append(issues, res.Issues...)

	d := validateData{
		pageData:  s.basePageData(),
		SpecCount: len(paths),
	}
	for _, iss := range issues {
		switch iss.Severity {
		case specfmt.SeverityError:
			d.Errors++
		case specfmt.SeverityWarning:
			d.Warnings++
		}
		d.Issues = append(d.Issues, validateIssue{
			File:     trimWorkspacePrefix(iss.File, s.opts.WorkspaceRoot),
			Path:     iss.Path,
			Category: iss.Category,
			Message:  iss.Message,
			Severity: iss.Severity.String(),
		})
	}
	s.render(w, r, "validate.tmpl", d)
}

// trimWorkspacePrefix shortens an absolute path inside the
// workspace to a relative-from-workspace form so the result
// table stays readable. Falls back to the absolute path when the
// file lives outside the workspace.
func trimWorkspacePrefix(p, root string) string {
	if root == "" {
		return p
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}
