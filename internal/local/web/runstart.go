package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/core/specfmt"
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

	// FromTask is the optional <spec-id>.<task-id> the run is
	// attached to (execution.RUN.1.1). Submitted by the harness
	// panel's spec/task dropdown — empty when the user picks
	// the blank option or the workspace has no specs. The
	// shell panel doesn't expose this field; ad-hoc shell runs
	// rarely benefit from spec attachment, and authors who
	// genuinely want it can use `rex run start --from-task` on
	// the CLI which has tab completion.
	FromTask string
	// FromTaskGroups groups every <spec-id>.<task-id> pair in
	// the workspace by their spec so the form template can
	// render a native <optgroup> per spec. At small scale
	// (1-10 specs) it's just visual sectioning; at larger
	// scale (50+ specs, hundreds of tasks) the grouping is the
	// only thing that keeps the dropdown scannable. Sorted by
	// spec id, then by task id within each group.
	FromTaskGroups []taskGroup

	Harnesses    []harnessFormOption
	ModelOptions []string
	ModeOptions  []string
	ShowModel    bool
	ShowMode     bool
}

// taskOption is one entry inside a taskGroup's dropdown
// section. Value is the wire form (`<spec-id>.<task-id>`)
// submitted on form post; Label is what the user sees in the
// dropdown row (`task-id · state · short description`).
// Description carries the full untruncated text so the form
// can render it in a "selected task" info panel below the
// dropdown, where there's room to show the whole thing.
type taskOption struct {
	Value       string
	Label       string
	State       string
	Description string
}

// taskGroup is one spec's worth of tasks rendered as an
// <optgroup>. SpecID is the kebab id (also the optgroup label
// suffix); SpecName is the human-readable metadata.name. Tasks
// is the spec's task list, already truncated/sorted at load
// time so the template stays tag-soup-free.
type taskGroup struct {
	SpecID   string
	SpecName string
	Tasks    []taskOption
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

// defaultHarnessName returned a preselected harness so the form
// rendered with a working default. The behaviour was misleading
// — users saw "opencode" already chosen and submitted it without
// realising it wasn't their intended target. Returning empty
// keeps the placeholder ("select a harness") visible until the
// author actively picks one.
func defaultHarnessName(_ []harnessFormOption) string {
	return ""
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
	d.FromTaskGroups = loadFromTaskGroups(s.opts.WorkspaceRoot)
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
//
// Query parameters prefill the spec-attachment fields so deep links
// from a spec page (or another tool) can land here with the
// linkage already typed:
//
//	/runs/new?from_task=execution.dag-primitives
//	/runs/new?spec_ref=execution.PRIM.6&spec_ref=execution.PRIM.5
func (s *Server) handleRunNew(w http.ResponseWriter, r *http.Request) {
	base := s.basePageData()
	if base.Workspace == nil {
		http.Error(w, "web: no workspace at "+s.opts.WorkspaceRoot, http.StatusInternalServerError)
		return
	}
	base.NavSection = "runs"
	// from_task may arrive prefilled via a deep link; pick the
	// run type up too so /runs/new?from_task=… defaults to the
	// harness panel where the field actually renders. Authors
	// who want the shell panel can flip the dropdown after.
	q := r.URL.Query()
	runType := strings.TrimSpace(q.Get("run_type"))
	if runType == "" {
		runType = "shell"
	}
	fromTask := strings.TrimSpace(q.Get("from_task"))
	if fromTask != "" && q.Get("run_type") == "" {
		runType = "harness"
	}
	d := s.prepareRunNewData(runNewData{
		pageData: base,
		RunType:  runType,
		FromTask: fromTask,
	})
	s.render(w, r, "run_new.tmpl", d)
}

// validateFromTaskField requires the standard <spec-id>.<task-id>
// shape: at least one dot, no internal whitespace, both halves
// non-empty. Empty input is fine — the field is optional.
//
// Kept around even though the form now uses a server-side
// dropdown — handleRunStart still revalidates the submitted
// value defensively, since a craftable POST or stale dropdown
// could otherwise let a malformed string through.
func validateFromTaskField(raw string) error {
	if raw == "" {
		return nil
	}
	idx := strings.Index(raw, ".")
	if idx <= 0 || idx == len(raw)-1 {
		return fmt.Errorf("from_task %q must be in <spec-id>.<task-id> form", raw)
	}
	if strings.ContainsAny(raw, " \t\n") {
		return fmt.Errorf("from_task %q must not contain whitespace", raw)
	}
	return nil
}

// loadFromTaskGroups walks the workspace's specs/ dir and
// returns one taskGroup per spec, each carrying that spec's
// tasks. Sorted by spec id, then by task id within each group.
// Specs that declare no tasks are dropped — an empty
// optgroup contributes nothing to the picker.
//
// Best-effort: failures (missing specs/ directory, unparseable
// spec) yield an empty list rather than blocking the form. The
// field is optional so "no groups" renders as the blank
// "(none)" entry only.
func loadFromTaskGroups(workspaceRoot string) []taskGroup {
	dir := filepath.Join(workspaceRoot, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []taskGroup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		doc, err := specfmt.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if len(doc.Tasks) == 0 {
			continue
		}
		group := taskGroup{
			SpecID:   doc.Metadata.ID,
			SpecName: doc.Metadata.Name,
			Tasks:    make([]taskOption, 0, len(doc.Tasks)),
		}
		for _, t := range doc.Tasks {
			// Row format: `task-id · state · description`. State
			// in the row lets authors scan by status; the
			// description gives at-a-glance context. Truncated to
			// keep wide descriptions from blowing out the picker.
			label := t.ID
			if t.State != "" {
				label += " · " + t.State
			}
			if t.Description != "" {
				label += " · " + truncate(t.Description, 80)
			}
			group.Tasks = append(group.Tasks, taskOption{
				Value:       doc.Metadata.ID + "." + t.ID,
				Label:       label,
				State:       t.State,
				Description: t.Description,
			})
		}
		sort.Slice(group.Tasks, func(i, j int) bool {
			return group.Tasks[i].Value < group.Tasks[j].Value
		})
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SpecID < out[j].SpecID })
	return out
}

// truncate caps a label at n characters; appends an ellipsis
// when it had to cut. Keeps dropdown rows from running across
// the page on chatty task descriptions.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
		RunType: strings.TrimSpace(r.FormValue("run_type")),
		// Interactive runs are now the web default: a harness
		// session that opens its own input prompt mid-run is
		// otherwise stuck waiting for a reply that never comes.
		// Authors who want the non-interactive shape use the
		// CLI's `rex run start --harness ... --prompt ...` flow.
		Interactive: true,
		Shell:       strings.TrimSpace(r.FormValue("shell")),
		Harness:     strings.TrimSpace(r.FormValue("harness")),
		Prompt:      strings.TrimSpace(r.FormValue("prompt")),
		Model:       strings.TrimSpace(r.FormValue("model")),
		Mode:        strings.TrimSpace(r.FormValue("mode")),
		FromTask:    strings.TrimSpace(r.FormValue("from_task")),
	})
	if err := validateFromTaskField(d.FromTask); err != nil {
		s.rerenderRunNew(w, r, err.Error(), d)
		return
	}

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
				Command:  argv,
				NodeID:   nodeID,
				RunID:    runID,
				FromTask: d.FromTask,
				OnEvent:  onEvent,
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
				FromTask: d.FromTask,
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
