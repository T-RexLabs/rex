package web

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
	internalweb "github.com/asabla/rex/internal/web"
)

// specsListData backs the specs_list.tmpl page.
type specsListData struct {
	pageData
	Specs []internalweb.SpecRow
}

// specDetailData backs the spec_detail.tmpl page. Composes the
// shared SpecContent (core spec data) with local-only extras
// (amendments, runs-by-task, harness dropdown options).
type specDetailData struct {
	pageData
	internalweb.SpecContent
	ActiveTab string
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
	// Amendments lists amendments whose amendment_for matches this
	// spec, surfaced in a panel on the rendered tab. Empty when
	// the workspace has no .rex/specs/_proposed/ directory or no
	// amendment files target this spec (web-ui.LOCAL.4.1).
	Amendments []amendmentRow
}

// localSpecProjection satisfies internalweb.SpecProjection by
// reading from <root>/.rex/specs/. It's the local shell's
// concrete implementation of the projection interface the shared
// /specs handlers query (web-ui.CENTRAL-LAYOUT.2).
type localSpecProjection struct{ root string }

func (l localSpecProjection) ListSpecs() ([]internalweb.SpecRow, error) {
	dir := filepath.Join(l.root, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]internalweb.SpecRow, 0, len(entries))
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
		out = append(out, internalweb.SpecRow{
			ID:             doc.Metadata.ID,
			Name:           doc.Metadata.Name,
			State:          doc.Metadata.State,
			TaskCount:      len(doc.Tasks),
			ComponentCount: len(doc.Components),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (l localSpecProjection) OpenSpec(id string) (*specfmt.Document, string, bool, error) {
	path := filepath.Join(l.root, ".rex", "specs", id+".yaml")
	doc, err := specfmt.ParseFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("parse spec %q: %w", id, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false, err
	}
	return doc, string(raw), true, nil
}

func loadSpecsList(opts Options) (specsListData, error) {
	base := newPageDataFromOpts(opts)
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := specsListData{pageData: base}
	rows, err := localSpecProjection{root: opts.WorkspaceRoot}.ListSpecs()
	if err != nil {
		return d, err
	}
	d.Specs = rows
	return d, nil
}

// loadSpecDetail looks up the spec via the shared loader, then
// composes local-only extras (amendments + runs-by-task) on top.
//
// hl is the chroma highlighter; when non-nil, RawYAML is also
// rendered to YAMLPretty so the source tab can show the
// highlighted view alongside (or instead of) the plain text.
func loadSpecDetail(opts Options, id, tab string, hl *internalweb.Highlighter) (specDetailData, bool, error) {
	content, found, err := internalweb.LoadSpecContent(localSpecProjection{root: opts.WorkspaceRoot}, id, hl)
	if err != nil || !found {
		return specDetailData{}, found, err
	}

	if tab == "" {
		tab = "rendered"
	}
	switch tab {
	case "rendered", "ask", "source", "tasks", "runs":
	default:
		tab = "rendered"
	}

	base := newPageDataFromOpts(opts)
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := specDetailData{
		pageData:    base,
		SpecContent: content,
		ActiveTab:   tab,
	}
	// Best-effort: list amendments targeting this spec. A missing
	// _proposed/ directory yields nil rather than an error
	// (web-ui.LOCAL.4.1).
	d.Amendments = loadAmendmentsForSpec(opts.WorkspaceRoot, content.Spec.ID)

	// Best-effort run lookup. Failures here (missing events.log
	// on a fresh workspace, parse error mid-log) shouldn't
	// 500 the spec page — we just skip the affordance.
	if byTask, untasked, err := loadRunsByTaskID(opts.WorkspaceRoot, content.Spec.ID); err == nil {
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
