package web

import (
	"html/template"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// SpecRow is one row in the specs list page. Carries just the
// metadata the list view renders; the full Document is read lazily
// on the detail page (web-ui.SHARED.1 spec_row partial).
type SpecRow struct {
	ID             string
	Name           string
	State          string
	TaskCount      int
	ComponentCount int
}

// SpecProjection is the read-side surface the shared spec
// handlers query. Local resolvers wrap filesystem reads under
// `<Root>/.rex/specs/`; central resolvers wrap GitStore reads
// keyed by `specs/<id>.yaml` (web-ui.CENTRAL-LAYOUT.2). The
// projection deliberately does not include amendments, runs, or
// other per-spec extras — those land with their own sub-tasks
// (central-read-side-search-amendments / central-read-side-runs-audit).
type SpecProjection interface {
	// ListSpecs returns metadata for every spec in the workspace
	// (parseable specs only — unparseable ones are skipped; the
	// `rex spec validate` surface is where authors fix them).
	// Empty workspaces return an empty slice and a nil error.
	ListSpecs() ([]SpecRow, error)

	// OpenSpec returns the parsed Document + raw YAML for the spec
	// identified by id, or (nil, "", false, nil) when the spec is
	// not present. A non-nil error is returned only on storage
	// failures the caller should surface as a 500.
	OpenSpec(id string) (doc *specfmt.Document, raw string, found bool, err error)
}

// SpecView is the flattened, template-friendly projection of a
// parsed spec. Templates reach for top-level fields like .Spec.ID
// rather than .Spec.Metadata.ID, which the parsed Document
// doesn't expose directly.
//
// DescriptionParas is the description split into prose
// paragraphs so the template can render <p> tags without
// preserving the YAML literal-block line breaks.
type SpecView struct {
	ID               string
	Name             string
	State            string
	CreatedAt        string
	UpdatedAt        string
	Description      string
	DescriptionParas []string
	Tasks            []TaskView
	Components       map[string]specfmt.Component
	ComponentOrder   []string
	Constraints      map[string]specfmt.Constraint
	ConstraintOrder  []string
}

// TaskView wraps a spec task for rendering. Recipe-presence and
// recipe-kind are pulled out so the template can decide whether to
// surface a "Run this task" affordance without reaching into the
// nested recipe object.
type TaskView struct {
	specfmt.Task
	HasRecipe  bool
	RecipeKind string
}

// NewSpecView projects a parsed Document into the template-shape.
// Returns nil when doc is nil so callers can drive a "not found"
// branch without a second check.
func NewSpecView(doc *specfmt.Document) *SpecView {
	if doc == nil {
		return nil
	}
	tasks := make([]TaskView, len(doc.Tasks))
	for i, t := range doc.Tasks {
		tv := TaskView{Task: t}
		if t.Run != nil {
			tv.HasRecipe = true
			tv.RecipeKind = string(t.Run.Kind)
		}
		tasks[i] = tv
	}
	return &SpecView{
		ID:               doc.Metadata.ID,
		Name:             doc.Metadata.Name,
		State:            doc.Metadata.State,
		CreatedAt:        doc.Metadata.CreatedAt,
		UpdatedAt:        doc.Metadata.UpdatedAt,
		Description:      doc.Description,
		DescriptionParas: SplitParagraphs(doc.Description),
		Tasks:            tasks,
		Components:       doc.Components,
		ComponentOrder:   doc.ComponentOrder(),
		Constraints:      doc.Constraints,
		ConstraintOrder:  doc.ConstraintOrder(),
	}
}

// SplitParagraphs converts a YAML literal-block description into
// prose paragraphs. Splits on blank lines (\n\n+); within each
// paragraph, collapses runs of whitespace (newlines + spaces +
// tabs) to a single space. Empty input yields nil. Authors who
// want hard line breaks within a paragraph still get them when
// they use double newlines explicitly.
func SplitParagraphs(s string) []string {
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

// SpecContent is the shared per-spec content the detail handlers
// need: the projected view + the raw YAML + the chroma-highlighted
// view. Page-specific extras (amendments, runs-by-task, harness
// dropdown options) are composed by each shell on top.
type SpecContent struct {
	Spec       *SpecView
	RawYAML    string
	YAMLPretty template.HTML
}

// LoadSpecContent is the shared spec-detail data loader. Returns
// (SpecContent{}, false, nil) when id is not kebab or the spec
// does not exist; the caller surfaces a 404 in both cases. When
// hl is non-nil the raw YAML is also rendered to YAMLPretty for
// the source tab.
func LoadSpecContent(p SpecProjection, id string, hl *Highlighter) (SpecContent, bool, error) {
	if !specfmt.IsKebab(id) {
		return SpecContent{}, false, nil
	}
	doc, raw, found, err := p.OpenSpec(id)
	if err != nil {
		return SpecContent{}, false, err
	}
	if !found {
		return SpecContent{}, false, nil
	}
	c := SpecContent{
		Spec:    NewSpecView(doc),
		RawYAML: raw,
	}
	if hl != nil {
		c.YAMLPretty = hl.HighlightYAML(raw)
	}
	return c, true, nil
}
