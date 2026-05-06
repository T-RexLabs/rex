package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/runtask"
)

// runNewData backs the run_new.tmpl form page.
type runNewData struct {
	pageData
	RunType     string
	Interactive bool
	Error       string
	Shell       string
	Harness     string
	Prompt      string
	Model       string
	Mode        string

	Harnesses    []harnessFormOption
	ModelOptions []string
	ModeOptions  []string
	ShowModel    bool
	ShowMode     bool
}

func anyHarnessModels(opts []harnessFormOption) bool {
	for _, opt := range opts {
		if len(opt.Models) > 0 {
			return true
		}
	}
	return false
}

func anyHarnessModes(opts []harnessFormOption) bool {
	for _, opt := range opts {
		if len(opt.Modes) > 0 {
			return true
		}
	}
	return false
}

func (s *Server) harnessRegistry() *adapter.Registry {
	return normalizeHarnessRegistry(s.opts.Adapters)
}

func defaultHarnessName(opts []harnessFormOption) string {
	for _, opt := range opts {
		if opt.Name == "opencode" {
			return opt.Name
		}
	}
	if len(opts) == 0 {
		return ""
	}
	return opts[0].Name
}

func findHarnessOption(opts []harnessFormOption, name string) (harnessFormOption, bool) {
	for _, opt := range opts {
		if opt.Name == name {
			return opt, true
		}
	}
	return harnessFormOption{}, false
}

func (s *Server) prepareRunNewData(d runNewData) runNewData {
	if d.RunType == "" {
		d.RunType = "shell"
	}
	d.Harnesses = s.harnesses.snapshot()
	if d.Harness == "" {
		d.Harness = defaultHarnessName(d.Harnesses)
	}
	selected, ok := findHarnessOption(d.Harnesses, d.Harness)
	if !ok {
		d.Harness = defaultHarnessName(d.Harnesses)
		selected, _ = findHarnessOption(d.Harnesses, d.Harness)
	}
	d.ModelOptions = append([]string(nil), selected.Models...)
	d.ModeOptions = append([]string(nil), selected.Modes...)
	d.ShowModel = anyHarnessModels(d.Harnesses)
	d.ShowMode = anyHarnessModes(d.Harnesses)
	return d
}

func (s *Server) rerenderRunNew(w http.ResponseWriter, r *http.Request, msg string, d runNewData) {
	base := s.basePageData()
	base.NavSection = "runs"
	d.pageData = base
	d.Error = msg
	d = s.prepareRunNewData(d)
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, r, "run_new.tmpl", d)
}

func (s *Server) launchRunAsync(ws *runtask.Workspace, start func(func(eventlog.Record)) error) (<-chan struct{}, <-chan error) {
	startedCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	var started atomic.Bool
	release := s.ownedRuns.start()
	go func() {
		defer release()
		defer ws.Close()
		onEvent := func(eventlog.Record) {
			if started.CompareAndSwap(false, true) {
				startedCh <- struct{}{}
			}
		}
		if err := start(onEvent); err != nil {
			if !started.Load() {
				errCh <- err
			}
			return
		}
		if !started.Load() {
			errCh <- fmt.Errorf("run finished before emitting any events")
		}
	}()
	return startedCh, errCh
}

func defaultNodeID(runType string) string {
	if runType == "harness" {
		return "harness"
	}
	return "shell"
}

// handleRunNew renders the GET /runs/new form. The start action now
// returns as soon as the run's first event is durably written, then
// redirects to the live run page so the user sees progress instead of
// waiting on a blank form submit.
func (s *Server) handleRunNew(w http.ResponseWriter, r *http.Request) {
	base := s.basePageData()
	if base.Workspace == nil {
		http.Error(w, "web: no workspace at "+s.opts.WorkspaceRoot, http.StatusInternalServerError)
		return
	}
	base.NavSection = "runs"
	d := s.prepareRunNewData(runNewData{pageData: base, RunType: "shell"})
	s.render(w, r, "run_new.tmpl", d)
}

// handleRunStart validates the submitted form, launches the run in a
// server-bound goroutine, waits until the first event is written, then
// redirects to the run detail page so the user can watch the run live.
func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	d := s.prepareRunNewData(runNewData{
		RunType:     strings.TrimSpace(r.FormValue("run_type")),
		Interactive: strings.TrimSpace(r.FormValue("interactive")) == "1",
		Shell:       strings.TrimSpace(r.FormValue("shell")),
		Harness:     strings.TrimSpace(r.FormValue("harness")),
		Prompt:      strings.TrimSpace(r.FormValue("prompt")),
		Model:       strings.TrimSpace(r.FormValue("model")),
		Mode:        strings.TrimSpace(r.FormValue("mode")),
	})

	switch d.RunType {
	case "shell":
		if d.Shell == "" {
			s.rerenderRunNew(w, r, "shell command is required", d)
			return
		}
	case "harness":
		if d.Harness == "" {
			s.rerenderRunNew(w, r, "harness is required", d)
			return
		}
		if d.Prompt == "" {
			s.rerenderRunNew(w, r, "prompt is required", d)
			return
		}
		if _, ok := s.harnessRegistry().Lookup(d.Harness); !ok {
			s.rerenderRunNew(w, r, fmt.Sprintf("unknown harness %q", d.Harness), d)
			return
		}
	default:
		s.rerenderRunNew(w, r, fmt.Sprintf("unknown run type %q", d.RunType), d)
		return
	}

	ws, err := runtask.Open(s.opts.WorkspaceRoot)
	if err != nil {
		http.Error(w, "web: open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	runID := ws.Clock.Now().String()
	nodeID := defaultNodeID(d.RunType)

	var startedCh <-chan struct{}
	var errCh <-chan error
	switch d.RunType {
	case "shell":
		argv, err := runtask.SplitShellCommand(d.Shell)
		if err != nil {
			_ = ws.Close()
			s.rerenderRunNew(w, r, err.Error(), d)
			return
		}
		startedCh, errCh = s.launchRunAsync(ws, func(onEvent func(eventlog.Record)) error {
			_, err := runtask.StartShellRun(s.ctx, ws, runtask.ShellRunRequest{
				Command: argv,
				NodeID:  nodeID,
				RunID:   runID,
				OnEvent: onEvent,
			})
			return err
		})
	case "harness":
		reg := s.harnessRegistry()
		s.interactions.registerWithPrompt(runID, d.Interactive, d.Prompt)
		startedCh, errCh = s.launchRunAsync(ws, func(onEvent func(eventlog.Record)) error {
			defer s.interactions.unregister(runID)
			var onInput func(context.Context, string) (string, error)
			if d.Interactive {
				onInput = func(ctx context.Context, _ string) (string, error) {
					return s.interactions.awaitInput(ctx, runID)
				}
			}
			_, err := runtask.StartHarnessRun(s.ctx, ws, runtask.HarnessRunRequest{
				Harness:  d.Harness,
				Prompt:   d.Prompt,
				Model:    d.Model,
				Mode:     d.Mode,
				NodeID:   nodeID,
				RunID:    runID,
				Adapters: reg,
				OnEvent:  onEvent,
				OnInput:  onInput,
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
	}

	select {
	case <-startedCh:
		http.Redirect(w, r, "/runs/"+runID, http.StatusSeeOther)
	case err := <-errCh:
		s.rerenderRunNew(w, r, fmt.Sprintf("run failed to start: %s", err), d)
	}
}
