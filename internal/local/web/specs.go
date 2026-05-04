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
type specView struct {
	ID              string
	Name            string
	State           string
	CreatedAt       string
	UpdatedAt       string
	Description     string
	Tasks           []specfmt.Task
	Components      map[string]specfmt.Component
	ComponentOrder  []string
	Constraints     map[string]specfmt.Constraint
	ConstraintOrder []string
}

func newSpecView(doc *specfmt.Document) *specView {
	if doc == nil {
		return nil
	}
	return &specView{
		ID:              doc.Metadata.ID,
		Name:            doc.Metadata.Name,
		State:           doc.Metadata.State,
		CreatedAt:       doc.Metadata.CreatedAt,
		UpdatedAt:       doc.Metadata.UpdatedAt,
		Description:     doc.Description,
		Tasks:           doc.Tasks,
		Components:      doc.Components,
		ComponentOrder:  doc.ComponentOrder(),
		Constraints:     doc.Constraints,
		ConstraintOrder: doc.ConstraintOrder(),
	}
}

// specDetailData backs the spec_detail.tmpl page.
type specDetailData struct {
	pageData
	Spec      *specView
	RawYAML   string
	ActiveTab string
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
func loadSpecDetail(opts Options, id, tab string) (specDetailData, bool, error) {
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

	return specDetailData{
		pageData:  base,
		Spec:      newSpecView(doc),
		RawYAML:   string(raw),
		ActiveTab: tab,
	}, true, nil
}
