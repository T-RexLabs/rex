package specfmt

import (
	"errors"
	"fmt"
	"time"
)

// Extra-key conventions a template uses to declare itself
// (spec-format.TMPL.1, .2). Stable strings so tooling can
// round-trip them.
const (
	ExtraTemplate          = "template"           // boolean: this spec IS a template
	ExtraTemplateID        = "template_id"        // string: this spec opts into a named template
	ExtraDefaultTemplateID = "default_template_id" // string (workspace-level): default template
	ExtraAppliesTo         = "applies_to"         // string: "workspace" | "org"
	ExtraRequiredExtra     = "required_extra"     // []string: keys non-template specs must have
)

// IsTemplate reports whether doc is itself a template spec.
func IsTemplate(doc *Document) bool {
	if doc == nil {
		return false
	}
	v, ok := doc.Extra[ExtraTemplate]
	if !ok {
		return false
	}
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// templateID returns the template a non-template spec opts into via
// extra.template_id. Empty when the spec is itself a template or
// when no opt-in is set.
func templateID(doc *Document) string {
	if doc == nil || IsTemplate(doc) {
		return ""
	}
	if v, ok := doc.Extra[ExtraTemplateID]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// requiredExtraKeys reads a template's required_extra list.
func requiredExtraKeys(t *Document) []string {
	if t == nil {
		return nil
	}
	v, ok := t.Extra[ExtraRequiredExtra]
	if !ok {
		return nil
	}
	switch v := v.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	}
	return nil
}

// Templates returns the workspace's template specs.
func (w *Workspace) Templates() []*Document {
	out := make([]*Document, 0)
	for _, doc := range w.Specs() {
		if IsTemplate(doc) {
			out = append(out, doc)
		}
	}
	return out
}

// Template returns the template with the given metadata.id, or nil.
func (w *Workspace) Template(id string) *Document {
	for _, t := range w.Templates() {
		if t.Metadata.ID == id {
			return t
		}
	}
	return nil
}

// ScaffoldOptions configure NewSpecFromTemplate.
type ScaffoldOptions struct {
	// ID is the metadata.id for the new spec. Required.
	ID string
	// Name is the metadata.name; defaults to ID when empty.
	Name string
	// State is the metadata.state; defaults to "draft".
	State string
	// Now returns the timestamp for created_at / updated_at.
	// Defaults to time.Now (UTC).
	Now func() time.Time
	// Template (optional) is the source template spec to inherit
	// shape from. When nil, a minimal skeleton is generated.
	Template *Document
}

// NewSpecFromTemplate builds a fresh Document scaffolded from the
// supplied options. Metadata is replaced (id, name, state,
// created_at). Template-marker extras (template / applies_to /
// required_extra) are stripped from the new spec — a scaffolded
// spec is not itself a template. The template_id link is set so the
// validator's second pass routes back to the source.
func NewSpecFromTemplate(opts ScaffoldOptions) (*Document, error) {
	if opts.ID == "" {
		return nil, errors.New("specfmt: NewSpecFromTemplate requires ID")
	}
	if !IsKebab(opts.ID) {
		return nil, fmt.Errorf("specfmt: id %q is not kebab-case", opts.ID)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	state := opts.State
	if state == "" {
		state = "draft"
	}
	name := opts.Name
	if name == "" {
		name = opts.ID
	}

	doc := &Document{
		SpecVersion: 1,
		Metadata: Metadata{
			ID:        opts.ID,
			Name:      name,
			State:     state,
			CreatedAt: now().Format(time.RFC3339),
			UpdatedAt: now().Format(time.RFC3339),
		},
	}

	if opts.Template != nil {
		doc.Description = opts.Template.Description
		doc.Tasks = cloneTasks(opts.Template.Tasks)
		doc.Components = cloneComponents(opts.Template.Components)
		doc.Constraints = cloneConstraints(opts.Template.Constraints)
		doc.Extra = scaffoldExtra(opts.Template.Extra, opts.Template.Metadata.ID)
	}
	return doc, nil
}

// scaffoldExtra strips template-marker keys (template, applies_to,
// required_extra) from the scaffolded spec's extra and adds a back-
// reference to the template via template_id.
func scaffoldExtra(src map[string]any, templateID string) map[string]any {
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		switch k {
		case ExtraTemplate, ExtraAppliesTo, ExtraRequiredExtra,
			ExtraDefaultTemplateID:
			continue
		}
		out[k] = v
	}
	if templateID != "" {
		out[ExtraTemplateID] = templateID
	}
	return out
}

func cloneTasks(in []Task) []Task {
	if in == nil {
		return nil
	}
	out := make([]Task, len(in))
	for i, t := range in {
		out[i] = t
		if t.References != nil {
			out[i].References = append([]string(nil), t.References...)
		}
	}
	return out
}

func cloneComponents(in map[string]Component) map[string]Component {
	if in == nil {
		return nil
	}
	out := make(map[string]Component, len(in))
	for k, c := range in {
		copy := c
		copy.Requirements = cloneRequirements(c.Requirements)
		out[k] = copy
	}
	return out
}

func cloneConstraints(in map[string]Constraint) map[string]Constraint {
	if in == nil {
		return nil
	}
	out := make(map[string]Constraint, len(in))
	for k, c := range in {
		copy := c
		copy.Requirements = cloneRequirements(c.Requirements)
		out[k] = copy
	}
	return out
}

func cloneRequirements(in map[string]Requirement) map[string]Requirement {
	if in == nil {
		return nil
	}
	out := make(map[string]Requirement, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
