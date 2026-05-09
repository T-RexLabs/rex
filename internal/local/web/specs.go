package web

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// specRow is one row in the /specs list page.
type specRow struct {
	ID             string
	Name           string
	State          string
	TaskCount      int
	ComponentCount int
}

// specsListData backs the specs_list.tmpl page.
type specsListData struct {
	pageData
	Specs []specRow
}

// specView is the flattened, template-friendly projection of a
// parsed spec. Templates reach for top-level fields like .Spec.ID
// rather than .Spec.Metadata.ID, which the parsed Document
// doesn't expose directly.
//
// DescriptionParas is the description split into prose paragraphs
// so the template can render <p> tags without preserving the
// YAML literal-block line breaks. Authors using description: |
// usually wrap source lines around column 70-80; that's a YAML
// readability convention, not an instruction to break the prose
// at those columns. We collapse single newlines to spaces and
// preserve blank lines as paragraph breaks.
type specView struct {
	ID               string
	Name             string
	State            string
	CreatedAt        string
	UpdatedAt        string
	Description      string
	DescriptionParas []string
	Tasks            []taskView
	Components       map[string]specfmt.Component
	ComponentOrder   []string
	Constraints      map[string]specfmt.Constraint
	ConstraintOrder  []string
}

// taskView wraps a spec task for rendering. Recipe-presence and
// recipe-kind are pulled out so the template can decide whether to
// surface a "Run this task" affordance without reaching into the
// nested recipe object.
type taskView struct {
	specfmt.Task
	HasRecipe  bool
	RecipeKind string
}

func newSpecView(doc *specfmt.Document) *specView {
	if doc == nil {
		return nil
	}
	tasks := make([]taskView, len(doc.Tasks))
	for i, t := range doc.Tasks {
		tv := taskView{Task: t}
		if t.Run != nil {
			tv.HasRecipe = true
			tv.RecipeKind = string(t.Run.Kind)
		}
		tasks[i] = tv
	}
	return &specView{
		ID:               doc.Metadata.ID,
		Name:             doc.Metadata.Name,
		State:            doc.Metadata.State,
		CreatedAt:        doc.Metadata.CreatedAt,
		UpdatedAt:        doc.Metadata.UpdatedAt,
		Description:      doc.Description,
		DescriptionParas: splitParagraphs(doc.Description),
		Tasks:            tasks,
		Components:       doc.Components,
		ComponentOrder:   doc.ComponentOrder(),
		Constraints:      doc.Constraints,
		ConstraintOrder:  doc.ConstraintOrder(),
	}
}

// splitParagraphs converts a YAML literal-block description into
// prose paragraphs. Splits on blank lines (\n\n+); within each
// paragraph, collapses runs of whitespace (newlines + spaces +
// tabs) to a single space. Empty input yields nil. Authors who
// want hard line breaks within a paragraph still get them when
// they use double newlines explicitly.
func splitParagraphs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		out = append(out, strings.Join(fields, " "))
	}
	return out
}

// specDetailData backs the spec_detail.tmpl page.
type specDetailData struct {
	pageData
	Spec       *specView
	RawYAML    string
	YAMLPretty template.HTML // chroma-highlighted view of RawYAML
	ActiveTab  string
	// RunsByTask maps task ids to runs launched from that task,
	// most-recent-first. Empty when the events.log has no
	// matching runs. Phase-C surface; the template uses it to
	// render a status dot + recent-run links per task.
	RunsByTask map[string][]runRow
	// UntaskedRuns are runs that cite the spec via spec_refs but
	// without naming a task — surfaced in the runs link in the
	// header rather than per task.
	UntaskedRuns []runRow
	// AllRuns is the flat union of every run that cites this
	// spec (per-task + untasked), sorted most-recent-first. Backs
	// the dedicated "runs" tab; the per-task and untasked
	// breakdowns above remain for the tasks tab.
	AllRuns []runRow
	// RunCount is the count rendered in the runs tab badge.
	RunCount int
	// Harnesses populates the harness dropdown on the ad-hoc
	// ask/amend form. Empty when no adapters are registered (the
	// runs tab shows a "no harnesses" hint instead of the form).
	Harnesses []harnessFormOption
}

func loadSpecsList(opts Options) (specsListData, error) {
	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := specsListData{pageData: base}
	dir := filepath.Join(opts.WorkspaceRoot, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return d, nil
		}
		return d, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		doc, err := specfmt.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			// Skip unparseable specs; rex spec validate is the
			// surface where the user fixes them.
			continue
		}
		d.Specs = append(d.Specs, specRow{
			ID:             doc.Metadata.ID,
			Name:           doc.Metadata.Name,
			State:          doc.Metadata.State,
			TaskCount:      len(doc.Tasks),
			ComponentCount: len(doc.Components),
		})
	}
	sort.Slice(d.Specs, func(i, j int) bool { return d.Specs[i].ID < d.Specs[j].ID })
	return d, nil
}

// loadSpecDetail looks up the spec by its kebab id, parses it, and
// reads the raw YAML so the source tab can render verbatim.
//
// hl is the chroma highlighter; when non-nil, RawYAML is also
// rendered to YAMLPretty so the source tab can show the
// highlighted view alongside (or instead of) the plain text.
func loadSpecDetail(opts Options, id, tab string, hl *Highlighter) (specDetailData, bool, error) {
	if !specfmt.IsKebab(id) {
		return specDetailData{}, false, nil
	}
	path := filepath.Join(opts.WorkspaceRoot, ".rex", "specs", id+".yaml")
	doc, err := specfmt.ParseFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return specDetailData{}, false, nil
		}
		return specDetailData{}, false, fmt.Errorf("parse spec %q: %w", id, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return specDetailData{}, false, err
	}

	if tab == "" {
		tab = "rendered"
	}
	switch tab {
	case "rendered", "ask", "source", "tasks", "runs":
	default:
		tab = "rendered"
	}

	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := specDetailData{
		pageData:  base,
		Spec:      newSpecView(doc),
		RawYAML:   string(raw),
		ActiveTab: tab,
	}
	if hl != nil {
		d.YAMLPretty = hl.HighlightYAML(string(raw))
	}
	// Best-effort run lookup. Failures here (missing events.log
	// on a fresh workspace, parse error mid-log) shouldn't
	// 500 the spec page — we just skip the affordance.
	if byTask, untasked, err := loadRunsByTaskID(opts.WorkspaceRoot, doc.Metadata.ID); err == nil {
		d.RunsByTask = byTask
		d.UntaskedRuns = untasked
		// AllRuns is the merged + sorted union for the runs tab.
		// Walk byTask in deterministic order so the tab's table
		// stays stable across reloads.
		merged := make([]runRow, 0)
		for _, rs := range byTask {
			merged = append(merged, rs...)
		}
		merged = append(merged, untasked...)
		sortRunsDesc(merged)
		d.AllRuns = merged
		d.RunCount = len(merged)
	}
	return d, true, nil
}
