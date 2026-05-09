package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/recipe"
	"github.com/asabla/rex/internal/local/runtask"
)

// handleSpecTaskRun implements POST /specs/{id}/tasks/{taskID}/run.
// It resolves the named task's recipe (spec-format.RECIPE), launches
// the run with the resolved fields, and redirects to /runs/<id> when
// the first event is durably written.
func (s *Server) handleSpecTaskRun(w http.ResponseWriter, r *http.Request) {
	specID := r.PathValue("id")
	taskID := r.PathValue("taskID")
	if specID == "" || taskID == "" {
		http.NotFound(w, r)
		return
	}

	resolved, err := recipe.LoadFromTaskRef(s.opts.WorkspaceRoot, specID+"."+taskID, nil)
	if err != nil {
		// Not-found-ish errors render as 404 so the link from the
		// spec page surfaces a useful message; mismatch and other
		// errors render as 400.
		if errors.Is(err, recipe.ErrUnsupportedKind) {
			http.Error(w, "web: "+err.Error(), http.StatusNotImplemented)
			return
		}
		http.Error(w, "web: resolve recipe: "+err.Error(), http.StatusBadRequest)
		return
	}

	ws, err := runtask.Open(s.opts.WorkspaceRoot)
	if err != nil {
		http.Error(w, "web: open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	runID := ws.Clock.Now().String()

	var startedCh <-chan struct{}
	var errCh <-chan error
	switch resolved.Recipe.Kind {
	case specfmt.RecipeKindShell:
		startedCh, errCh = s.launchRunAsync(ws, func(onEvent func(eventlog.Record)) error {
			_, err := runtask.StartShellRun(s.ctx, ws, runtask.ShellRunRequest{
				Command:  resolved.Command,
				Dir:      taskRunCwd(s.opts.WorkspaceRoot, resolved.Recipe.Cwd),
				Env:      resolved.Recipe.Env,
				NodeID:   "shell",
				RunID:    runID,
				SpecRefs: resolved.SpecRefs,
				FromTask: resolved.FromTask,
				OnEvent:  onEvent,
			})
			return err
		})
	case specfmt.RecipeKindHarness, specfmt.RecipeKindSpecAction:
		// spec_action goes through the harness path — the recipe
		// loader has pre-pended the target spec's YAML to
		// resolved.Prompt (RECIPE.6) and folded the target into
		// resolved.SpecRefs.
		reg := s.harnessRegistry()
		if _, ok := reg.Lookup(resolved.Recipe.Harness); !ok {
			_ = ws.Close()
			http.Error(w, fmt.Sprintf("web: recipe references harness %q which has no adapter registered", resolved.Recipe.Harness), http.StatusBadRequest)
			return
		}
		s.interactions.registerWithPrompt(runID, false, resolved.Prompt)
		startedCh, errCh = s.launchRunAsync(ws, func(onEvent func(eventlog.Record)) error {
			defer s.interactions.unregister(runID)
			_, err := runtask.StartHarnessRun(s.ctx, ws, runtask.HarnessRunRequest{
				Harness:  resolved.Recipe.Harness,
				Prompt:   resolved.Prompt,
				NodeID:   "harness",
				RunID:    runID,
				Adapters: reg,
				SpecRefs: resolved.SpecRefs,
				FromTask: resolved.FromTask,
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
	default:
		_ = ws.Close()
		http.Error(w, "web: unsupported recipe kind", http.StatusNotImplemented)
		return
	}

	select {
	case <-startedCh:
		http.Redirect(w, r, "/runs/"+runID, http.StatusSeeOther)
	case err := <-errCh:
		http.Error(w, fmt.Sprintf("web: run failed to start: %s", err), http.StatusInternalServerError)
	}
}

// taskRunCwd resolves a recipe `cwd` against the workspace root.
func taskRunCwd(workspaceRoot, cwd string) string {
	if cwd == "" {
		return workspaceRoot
	}
	if isAbsPath(cwd) {
		return cwd
	}
	return workspaceRoot + "/" + cwd
}

func isAbsPath(p string) bool {
	return len(p) > 0 && p[0] == '/'
}
