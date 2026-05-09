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
	ExtraTemplate          = "template"            // boolean: this spec IS a template
	ExtraTemplateID        = "template_id"         // string: this spec opts into a named template
	ExtraDefaultTemplateID = "default_template_id" // string (workspace-level): default template
	ExtraAppliesTo         = "applies_to"          // string: "workspace" | "org"
	ExtraRequiredExtra     = "required_extra"      // []string: keys non-template specs must have
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
//
// Use this when opts.Template is non-nil. For the no-template
// "minimal skeleton" path use MinimalSkeletonYAML — it emits a
// hand-rolled YAML body with comments and placeholder fields
// that yaml.Marshal of an empty Document would strip.
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
			ID:           opts.ID,
			Name:         name,
			State:        state,
			Owners:       []string{},
			RelatedSpecs: []string{},
			CreatedAt:    now().Format(time.RFC3339),
			UpdatedAt:    now().Format(time.RFC3339),
		},
	}

	if opts.Template != nil {
		doc.Description = opts.Template.Description
		doc.Tasks = cloneTasks(opts.Template.Tasks)
		doc.Components = cloneComponents(opts.Template.Components)
		doc.Constraints = cloneConstraints(opts.Template.Constraints)
		doc.Extra = scaffoldExtra(opts.Template.Extra, opts.Template.Metadata.ID)
		// Inherit metadata.owners / related_specs from the
		// template too so org-default ownership flows through
		// to the new spec without a manual copy step.
		if len(opts.Template.Metadata.Owners) > 0 {
			doc.Metadata.Owners = append([]string{}, opts.Template.Metadata.Owners...)
		}
		if len(opts.Template.Metadata.RelatedSpecs) > 0 {
			doc.Metadata.RelatedSpecs = append([]string{}, opts.Template.Metadata.RelatedSpecs...)
		}
	}
	return doc, nil
}

// minimalSkeletonTemplate is the body emitted by `rex spec
// create <id>` (and the equivalent web flow) when no workspace
// or per-call template applies. Hand-rolled so the YAML carries
// inline comments — yaml.Marshal of an empty Document strips
// every block that doesn't have a value, leaving authors with
// just metadata. The minimal skeleton instead surfaces every
// field a v1 spec can carry, with a placeholder example task,
// component, and pointers into the format's component IDs so a
// new author can fill blanks rather than discover the schema
// from spec-format.yaml.
//
// Token replacements (no full Go-template rendering — keep this
// dependency-free):
//
//	%ID%         — opts.ID
//	%NAME%       — opts.Name (defaults to opts.ID)
//	%STATE%      — opts.State (defaults to "draft")
//	%CREATED_AT% — opts.Now() in RFC3339 (UTC)
//	%UPDATED_AT% — same
const minimalSkeletonTemplate = `spec_version: 1

metadata:
  id: %ID%
  name: %NAME%
  # state: one of draft, active, accepted, archived (spec-format.META.3).
  state: %STATE%
  # owners: list of user handles for this spec (spec-format.META.6).
  owners: []
  # related_specs: spec ids this document is meaningfully connected to (META.7).
  related_specs: []
  created_at: %CREATED_AT%
  updated_at: %UPDATED_AT%

description: |
  TODO — one paragraph on what this spec owns and why it exists.
  This block is rendered as Markdown on /specs/<id>.

# tasks: units of implementation work this spec embeds (spec-format.TASK).
# Required: id (kebab-case, unique), description, state (todo / in_progress
# / done / blocked). Optional: references (ACIDs into component requirements),
# assigned_to, note (free-form context), proof (required-structured when
# state is done — see VAL.7), depends_on (task ids in this spec), run
# (recipe — see RECIPE).
tasks:
  - id: example-task
    description: TODO — what someone would do to satisfy this task.
    state: todo
    references: []
    # note: free-form Markdown explaining context the description doesn't
    # capture — rationale, deferred sub-decisions, related amendments.
    note: ""
    # proof: short string (author scratchpad) OR a structured list of
    # entries the validator + ` + "`rex spec verify`" + ` mechanically check.
    # state: done MUST carry a structured list. Recognised kinds:
    #   - kind: code
    #     path: internal/foo/bar.go
    #   - kind: test
    #     path: internal/foo/bar_test.go
    #     name: TestThing            # optional Go test func to grep
    #   - kind: run
    #     run_id: <hlc-run-id>
    #   - kind: commit
    #     ref: <git-rev>
    #   - kind: spec
    #     acid: other-spec.COMP.1
    proof: []
    # depends_on: kebab-case task ids in this same spec that must reach
    # state: done before this task can be in_progress / done. Cycles are
    # rejected (VAL.10); state-ordering gates fire on transitions (VAL.11).
    depends_on: []

# components: groups of acceptance criteria (spec-format.COMP).
# IDs are uppercase ASCII + hyphens. Each component has a name and a
# requirements mapping (numbered keys, quoted to stay strings).
components:
  EXAMPLE:
    name: Example component
    requirements:
      "1": TODO — replace with the first acceptance criterion this spec asserts.

# constraints: cross-cutting invariants (engineering, security, performance).
# Same shape as components but uses ` + "`description`" + ` instead of ` + "`name`" + `.
# Drop in groups like ENG, SEC, PERF as needed.
constraints: {}

# extra: project-defined fields. owners + related_specs have moved to metadata;
# common keys here are notes, template (bool — this spec IS a template),
# template_id, recipes_required, recipes_forbidden.
extra:
  notes: ""
`

// MinimalSkeletonYAML emits the hand-rolled YAML scaffold for a
// new spec when no template is supplied. The output is parseable
// (specfmt.Parse round-trips it) and validator-clean (state:
// draft, the example task is in state: todo so VAL.7/8 don't
// trip).
func MinimalSkeletonYAML(opts ScaffoldOptions) ([]byte, error) {
	if opts.ID == "" {
		return nil, errors.New("specfmt: MinimalSkeletonYAML requires ID")
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

	stamp := now().Format(time.RFC3339)
	body := minimalSkeletonTemplate
	body = replaceToken(body, "%ID%", opts.ID)
	body = replaceToken(body, "%NAME%", name)
	body = replaceToken(body, "%STATE%", state)
	body = replaceToken(body, "%CREATED_AT%", stamp)
	body = replaceToken(body, "%UPDATED_AT%", stamp)
	return []byte(body), nil
}

// replaceToken is a tiny substitution helper. We keep this
// dependency-free instead of pulling text/template so the
// scaffold path doesn't grow a parsing surface for what is
// fundamentally a five-token replace.
func replaceToken(body, token, value string) string {
	for {
		idx := indexOf(body, token)
		if idx < 0 {
			return body
		}
		body = body[:idx] + value + body[idx+len(token):]
	}
}

func indexOf(s, sub string) int {
	if len(sub) == 0 || len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
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
