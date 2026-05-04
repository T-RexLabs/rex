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
	Tasks            []specfmt.Task
	Components       map[string]specfmt.Component
	ComponentOrder   []string
	Constraints      map[string]specfmt.Constraint
	ConstraintOrder  []string
}

func newSpecView(doc *specfmt.Document) *specView {
	if doc == nil {
		return nil
	}
	return &specView{
		ID:               doc.Metadata.ID,
		Name:             doc.Metadata.Name,
		State:            doc.Metadata.State,
		CreatedAt:        doc.Metadata.CreatedAt,
		UpdatedAt:        doc.Metadata.UpdatedAt,
		Description:      doc.Description,
		DescriptionParas: splitParagraphs(doc.Description),
		Tasks:            doc.Tasks,
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
	case "rendered", "source", "tasks":
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
	return d, true, nil
}
