package web

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/recipe"
	"github.com/asabla/rex/internal/local/runtask"
)

// handleSpecAdHocAction implements POST /specs/{id}/ask — the
// web counterpart to `rex spec ask` / `rex spec amend`. The
// form on /specs/<id>?tab=runs (and the tasks tab) submits
// here with `action`, `harness`, and `prompt` fields. The
// handler synthesises an in-memory spec_action recipe via the
// same buildSpecActionPrompt path the CLI uses, launches a
// harness run, and redirects to /runs/<id>.
//
// Why a single endpoint for both ask + amend: the only
// difference is the action enum, and that's a form field. One
// handler keeps the routing flat.
func (s *Server) handleSpecAdHocAction(w http.ResponseWriter, r *http.Request) {
	specID := r.PathValue("id")
	if specID == "" || !specfmt.IsKebab(specID) {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	harness := strings.TrimSpace(r.FormValue("harness"))
	prompt := strings.TrimSpace(r.FormValue("prompt"))

	if action == "" {
		action = "review"
	}
	if _, ok := recognizedAdHocActions[action]; !ok {
		http.Error(w, fmt.Sprintf("web: unknown action %q (expected ask, amend, draft)", action), http.StatusBadRequest)
		return
	}
	if harness == "" {
		http.Error(w, "web: harness is required", http.StatusBadRequest)
		return
	}
	reg := s.harnessRegistry()
	if _, ok := reg.Lookup(harness); !ok {
		http.Error(w, fmt.Sprintf("web: unknown harness %q", harness), http.StatusBadRequest)
		return
	}
	if prompt == "" {
		http.Error(w, "web: prompt is required", http.StatusBadRequest)
		return
	}

	root := s.opts.WorkspaceRoot
	if root == "" {
		http.Error(w, "web: no workspace root configured", http.StatusInternalServerError)
		return
	}
	specPath := filepath.Join(root, ".rex", "specs", specID+".yaml")
	doc, err := specfmt.ParseFile(specPath)
	if err != nil {
		http.Error(w, "web: load spec: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Synthesise the spec_action recipe + a fake host task so
	// PROMPT.1 token substitution still works the same way the
	// recipe-resolver path produces it.
	synthRecipe := &specfmt.Recipe{
		Kind:    specfmt.RecipeKindSpecAction,
		Action:  specfmt.SpecAction(action),
		Target:  specID,
		Harness: harness,
		Prompt:  prompt,
	}
	synthTask := &specfmt.Task{
		ID:          action,
		Description: prompt,
		State:       "in_progress",
	}
	fullPrompt, err := recipe.BuildSpecActionPrompt(root, synthRecipe, doc, synthTask)
	if err != nil {
		http.Error(w, "web: build prompt: "+err.Error(), http.StatusBadRequest)
		return
	}

	ws, err := runtask.Open(root)
	if err != nil {
		http.Error(w, "web: open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	runID := ws.Clock.Now().String()

	s.interactions.registerWithPrompt(runID, true, prompt)
	startedCh, errCh := s.launchRunAsync(ws, func(onEvent func(eventlog.Record)) error {
		defer s.interactions.unregister(runID)
		_, err := runtask.StartHarnessRun(s.ctx, ws, runtask.HarnessRunRequest{
			Harness:  harness,
			Prompt:   fullPrompt,
			NodeID:   "harness",
			RunID:    runID,
			Adapters: reg,
			SpecRefs: []string{specID},
			OnEvent:  onEvent,
			OnPermission: func(ctx context.Context, req runner.PermissionRequestedEvent) (runtask.PermissionResolution, error) {
				res, err := s.interactions.waitPermission(ctx, runID, req.RequestID)
				if err != nil {
					return runtask.PermissionResolution{}, err
				}
				return runtask.PermissionResolution{Granted: res.Granted, Note: res.Note}, nil
			},
		})
		return err
	})

	select {
	case <-startedCh:
		http.Redirect(w, r, "/runs/"+runID, http.StatusSeeOther)
	case err := <-errCh:
		http.Error(w, fmt.Sprintf("web: run failed to start: %s", err), http.StatusInternalServerError)
	}
}

// recognizedAdHocActions enumerates the action values the
// `/specs/{id}/ask` endpoint accepts. "review" maps to the CLI's
// `rex spec ask` (read-only commentary); "amend" to `rex spec
// amend` (YAML-amendment instruction); "draft" is also accepted
// for symmetry with the schema's RECIPE.6.1 enum.
var recognizedAdHocActions = map[string]struct{}{
	"review": {},
	"amend":  {},
	"draft":  {},
}
