package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/asabla/rex/internal/local/runtask"
)

// runNewData backs the run_new.tmpl form page.
type runNewData struct {
	pageData
	// Error is rendered above the form when a previous submit
	// failed validation. Empty on the GET path.
	Error string
	// Shell is the previously-submitted shell command, redisplayed
	// when validation failed so the user doesn't retype.
	Shell string
	// NodeID lets the user override the node id (default: shell).
	NodeID string
}

// handleRunNew renders the GET /runs/new form. The actual run
// dispatch happens at POST /runs/start (handleRunStart) so the URL
// shape matches the spec's "GET fetches a representation, POST
// creates" idiom — and a JS-disabled browser can submit the form
// the same way htmx does.
func (s *Server) handleRunNew(w http.ResponseWriter, r *http.Request) {
	base := s.basePageData()
	if base.Workspace == nil {
		http.Error(w, "web: no workspace at "+s.opts.WorkspaceRoot, http.StatusInternalServerError)
		return
	}
	base.NavSection = "runs"
	s.render(w, r, "run_new.tmpl", runNewData{
		pageData: base,
		NodeID:   "shell",
	})
}

// handleRunStart parses the form, dispatches a synchronous shell
// run via runtask.StartShellRun, and redirects to the run detail
// page on success. Validation failures rerender the form with the
// previously-typed values so the user doesn't lose state.
//
// Synchronous semantics match v1: there is no daemon, the run is
// the request goroutine's lifetime. If the user closes the tab
// mid-run the request context cancels and the run aborts. This is
// fine for the v1 daily-driver — long runs are launched from the
// CLI, the web form is for one-shot smoke tests.
func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	shell := strings.TrimSpace(r.FormValue("shell"))
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	if nodeID == "" {
		nodeID = "shell"
	}

	rerender := func(msg string) {
		base := s.basePageData()
		base.NavSection = "runs"
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "run_new.tmpl", runNewData{
			pageData: base,
			Error:    msg,
			Shell:    shell,
			NodeID:   nodeID,
		})
	}

	if shell == "" {
		rerender("shell command is required")
		return
	}
	argv, err := runtask.SplitShellCommand(shell)
	if err != nil {
		rerender(err.Error())
		return
	}

	ws, err := runtask.Open(s.opts.WorkspaceRoot)
	if err != nil {
		http.Error(w, "web: open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer ws.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	res, err := runtask.StartShellRun(ctx, ws, runtask.ShellRunRequest{
		Command: argv,
		NodeID:  nodeID,
	})
	if err != nil {
		// Best-effort surface: rerender with the error.
		rerender(fmt.Sprintf("run failed to start: %s", err))
		return
	}
	if res == nil || res.RunID == "" {
		rerender("run failed: empty result from runtask")
		return
	}

	// 303 See Other so the browser issues a GET on the redirect
	// target — POST/redirect/GET pattern, refreshes idempotent.
	http.Redirect(w, r, "/runs/"+res.RunID, http.StatusSeeOther)
}
