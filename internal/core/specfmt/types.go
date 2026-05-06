package specfmt

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Document is the top-level shape of a spec file (spec-format.CORE).
//
// Every field except SpecVersion and Metadata is optional. Extra is
// preserved as raw YAML so the validator can flag unknown top-level
// keys without specfmt needing to know every project-defined extension.
type Document struct {
	SpecVersion int                   `yaml:"spec_version"`
	Metadata    Metadata              `yaml:"metadata"`
	Description string                `yaml:"description,omitempty"`
	Tasks       []Task                `yaml:"tasks,omitempty"`
	Components  map[string]Component  `yaml:"components,omitempty"`
	Constraints map[string]Constraint `yaml:"constraints,omitempty"`
	Extra       map[string]any        `yaml:"extra,omitempty"`

	// Path is populated by ParseFile and surfaced in cross-spec
	// validation issues (Issue.File). Leave empty when constructing
	// a Document programmatically.
	Path string `yaml:"-"`

	// rawTopKeys retains the names of top-level keys actually present
	// on the wire. Lets the validator detect unknown top-level keys
	// per spec-format.CORE.3 even though the struct decoder silently
	// drops them.
	rawTopKeys []string `yaml:"-"`
	// componentOrder and constraintOrder preserve insertion order from
	// the source YAML so error reports walk in document order rather
	// than Go map iteration order.
	componentOrder  []string `yaml:"-"`
	constraintOrder []string `yaml:"-"`
}

// Metadata is the per-spec identity block (spec-format.META).
type Metadata struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	State     string `yaml:"state"`
	CreatedAt string `yaml:"created_at,omitempty"`
	UpdatedAt string `yaml:"updated_at,omitempty"`
}

// Task is one work item embedded in a spec (spec-format.TASK).
type Task struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	State       string   `yaml:"state"`
	References  []string `yaml:"references,omitempty"`
	AssignedTo  string   `yaml:"assigned_to,omitempty"`
	// Run is an optional recipe describing how to launch a run that
	// implements this task (spec-format.TASK.6 / spec-format.RECIPE).
	// Nil means the task has no canonical run; UI surfaces hide the
	// "Run this task" affordance for it.
	Run *Recipe `yaml:"run,omitempty"`
}

// RecipeKind enumerates the v1 recipe kinds (spec-format.RECIPE.1).
// Unknown kinds are surfaced by the validator per spec-format.TASK.6.1.
type RecipeKind string

const (
	RecipeKindShell        RecipeKind = "shell"
	RecipeKindSpecValidate RecipeKind = "spec_validate"
	RecipeKindHarness      RecipeKind = "harness"
)

// PermissionScope is the v1 enumerated value set for harness recipes
// (spec-format.RECIPE.4).
type PermissionScope string

const (
	PermissionScopeReadOnly     PermissionScope = "read_only"
	PermissionScopeWorkspace    PermissionScope = "workspace"
	PermissionScopeUnrestricted PermissionScope = "unrestricted"
)

// Recipe is the embedded run-launch hint attached to a Task
// (spec-format.RECIPE). Fields outside the recipe's Kind are ignored
// by the executor at resolution time but are validated for shape.
type Recipe struct {
	// Kind discriminates the field set. Required.
	Kind RecipeKind `yaml:"kind"`
	// Description optionally overrides the task description in UI
	// surfaces (spec-format.RECIPE.5).
	Description string `yaml:"description,omitempty"`

	// kind: shell
	Command []string          `yaml:"command,omitempty"`
	Cwd     string            `yaml:"cwd,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`

	// kind: spec_validate
	Paths       []string `yaml:"paths,omitempty"`
	Strict      *bool    `yaml:"strict,omitempty"`
	StrictUnset bool     `yaml:"-"`

	// kind: harness
	Harness         string          `yaml:"harness,omitempty"`
	Prompt          string          `yaml:"prompt,omitempty"`
	PermissionScope PermissionScope `yaml:"permission_scope,omitempty"`
}

// StrictValue reports whether the spec_validate recipe runs in strict
// mode (the default when `strict` is omitted, per spec-format.RECIPE.3).
func (r *Recipe) StrictValue() bool {
	if r == nil || r.Strict == nil {
		return true
	}
	return *r.Strict
}

// Component is one acceptance-criteria group (spec-format.COMP).
type Component struct {
	Name         string                 `yaml:"name"`
	Requirements map[string]Requirement `yaml:"requirements"`

	// requirementOrder preserves YAML insertion order for stable
	// validator output.
	requirementOrder []string `yaml:"-"`
}

// Constraint is one cross-cutting invariant group (spec-format.CONST).
// Constraints differ from Components only in using `description`
// instead of `name` (spec-format.CONST.3).
type Constraint struct {
	Description  string                 `yaml:"description"`
	Requirements map[string]Requirement `yaml:"requirements"`

	requirementOrder []string `yaml:"-"`
}

// Requirement is one numbered requirement line (spec-format.REQ).
//
// REQ.4 lets a value be either a plain string ("the requirement text")
// or a mapping with text/deprecated/replaced_by/notes keys. The
// UnmarshalYAML method handles both forms so callers always see the
// same struct.
type Requirement struct {
	Text       string `yaml:"text,omitempty"`
	Deprecated bool   `yaml:"deprecated,omitempty"`
	ReplacedBy string `yaml:"replaced_by,omitempty"`
	Notes      string `yaml:"notes,omitempty"`
}

// UnmarshalYAML accepts the plain-string short form and the full
// mapping form per spec-format.REQ.4.
func (r *Requirement) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		r.Text = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("specfmt: requirement value must be a string or a mapping, got %d", node.Kind)
	}
	type alias Requirement
	tmp := (*alias)(r)
	return node.Decode(tmp)
}

// MarshalYAML emits the compact form when only Text is set, and the
// mapping form otherwise. Round-trips that go through specfmt do not
// inflate plain strings into mappings.
func (r Requirement) MarshalYAML() (any, error) {
	if !r.Deprecated && r.ReplacedBy == "" && r.Notes == "" {
		return r.Text, nil
	}
	type alias Requirement
	return alias(r), nil
}

// ComponentOrder returns component IDs in YAML insertion order.
func (d *Document) ComponentOrder() []string {
	out := make([]string, len(d.componentOrder))
	copy(out, d.componentOrder)
	return out
}

// ConstraintOrder returns constraint IDs in YAML insertion order.
func (d *Document) ConstraintOrder() []string {
	out := make([]string, len(d.constraintOrder))
	copy(out, d.constraintOrder)
	return out
}

// TopLevelKeys returns the names of top-level keys that were physically
// present on the wire. Used by the validator's unknown-top-level-key
// check (spec-format.CORE.3).
func (d *Document) TopLevelKeys() []string {
	out := make([]string, len(d.rawTopKeys))
	copy(out, d.rawTopKeys)
	return out
}

// RequirementOrder returns requirement IDs in YAML insertion order.
func (c Component) RequirementOrder() []string {
	out := make([]string, len(c.requirementOrder))
	copy(out, c.requirementOrder)
	return out
}

// RequirementOrder returns requirement IDs in YAML insertion order.
func (c Constraint) RequirementOrder() []string {
	out := make([]string, len(c.requirementOrder))
	copy(out, c.requirementOrder)
	return out
}

// Parse decodes one Document from a YAML reader.
func Parse(r io.Reader) (*Document, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("specfmt: read: %w", err)
	}
	return parseBytes(body)
}

// ParseFile decodes one Document from a path on disk and stamps the
// path onto Document.Path so cross-spec validation can surface it.
func ParseFile(path string) (*Document, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("specfmt: read %s: %w", path, err)
	}
	doc, err := parseBytes(body)
	if err != nil {
		return nil, err
	}
	doc.Path = path
	return doc, nil
}

func parseBytes(body []byte) (*Document, error) {
	// Two passes: one into the struct (so callers get typed access),
	// one into a yaml.Node so we can recover top-level key order and
	// names that the struct decoder discards.
	var doc Document
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("specfmt: decode: %w", err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil {
		return nil, fmt.Errorf("specfmt: decode (raw): %w", err)
	}
	if err := annotateOrderAndKeys(&doc, &node); err != nil {
		return nil, err
	}
	return &doc, nil
}

// annotateOrderAndKeys walks the raw YAML node tree to populate
// rawTopKeys / componentOrder / constraintOrder / requirementOrder on
// the typed Document. The struct decoder loses ordering and unknown
// keys; we recover both here.
func annotateOrderAndKeys(doc *Document, node *yaml.Node) error {
	// Top-level node is a Document node wrapping a Mapping node.
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return nil
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i]
		v := root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		doc.rawTopKeys = append(doc.rawTopKeys, k.Value)
		switch k.Value {
		case "components":
			ids, perReq := collectGroupOrder(v)
			doc.componentOrder = ids
			for id, order := range perReq {
				if c, ok := doc.Components[id]; ok {
					c.requirementOrder = order
					doc.Components[id] = c
				}
			}
		case "constraints":
			ids, perReq := collectGroupOrder(v)
			doc.constraintOrder = ids
			for id, order := range perReq {
				if c, ok := doc.Constraints[id]; ok {
					c.requirementOrder = order
					doc.Constraints[id] = c
				}
			}
		}
	}
	return nil
}

// collectGroupOrder walks a "components" or "constraints" mapping node
// and returns:
//   - the group IDs in source order
//   - a per-group-ID list of requirement IDs in source order
func collectGroupOrder(group *yaml.Node) ([]string, map[string][]string) {
	if group == nil || group.Kind != yaml.MappingNode {
		return nil, nil
	}
	ids := make([]string, 0, len(group.Content)/2)
	perReq := make(map[string][]string)
	for i := 0; i+1 < len(group.Content); i += 2 {
		k := group.Content[i]
		v := group.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		ids = append(ids, k.Value)
		if v.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(v.Content); j += 2 {
			ck := v.Content[j]
			cv := v.Content[j+1]
			if ck.Kind != yaml.ScalarNode || ck.Value != "requirements" {
				continue
			}
			if cv.Kind != yaml.MappingNode {
				continue
			}
			reqIDs := make([]string, 0, len(cv.Content)/2)
			for r := 0; r+1 < len(cv.Content); r += 2 {
				if cv.Content[r].Kind == yaml.ScalarNode {
					reqIDs = append(reqIDs, cv.Content[r].Value)
				}
			}
			perReq[k.Value] = reqIDs
		}
	}
	return ids, perReq
}
