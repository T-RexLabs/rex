package specfmt

import (
	"fmt"
	"sort"
	"time"
)

// Mode controls how strictly Validate treats unknown keys (and any
// future "graceful degradation" knobs). spec-format.VAL.2.
type Mode int

const (
	// ModeStrict — unknown top-level keys and any other defects are
	// reported as errors. This is the default for `rex spec validate`.
	ModeStrict Mode = iota
	// ModeLenient — unknown top-level keys downgrade to warnings.
	// Used by read paths that need to keep working under schema
	// drift.
	ModeLenient
)

// Severity is the report level of one Issue.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

// String renders Severity for printable output.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	}
	return "unknown"
}

// Issue is one validation finding.
type Issue struct {
	// File is optional; callers normally set it to the path the
	// document was loaded from. Validate itself does not know.
	File string
	// Path is a YAML-flavoured dotted path (e.g.
	// "metadata.id", "tasks[2].references[0]", "components.AUTH").
	Path string
	// Category is a short kebab-case code so callers can filter or
	// suppress. Examples: "required-field", "format", "acid",
	// "unknown-key", "duplicate", "collision".
	Category string
	// Message is the human-readable explanation.
	Message string
	// Severity is error or warning.
	Severity Severity
}

// String formats one Issue in the canonical "[severity] file:path:
// category: message" shape spec-format.VAL.1 implies.
func (i Issue) String() string {
	loc := i.Path
	if i.File != "" {
		loc = i.File + ":" + loc
	}
	return fmt.Sprintf("[%s] %s: %s: %s", i.Severity, loc, i.Category, i.Message)
}

// Result groups every Issue from one Validate call.
type Result struct {
	Issues []Issue
}

// HasErrors reports whether at least one issue is at SeverityError.
func (r Result) HasErrors() bool {
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns just the error-severity issues.
func (r Result) Errors() []Issue {
	out := make([]Issue, 0, len(r.Issues))
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Warnings returns just the warning-severity issues.
func (r Result) Warnings() []Issue {
	out := make([]Issue, 0, len(r.Issues))
	for _, i := range r.Issues {
		if i.Severity == SeverityWarning {
			out = append(out, i)
		}
	}
	return out
}

// recognizedTopLevelKeys are the keys spec-format.CORE.2 enumerates.
var recognizedTopLevelKeys = map[string]struct{}{
	"spec_version": {},
	"metadata":     {},
	"description":  {},
	"tasks":        {},
	"components":   {},
	"constraints":  {},
	"extra":        {},
}

var validMetadataStates = map[string]struct{}{
	"draft":    {},
	"active":   {},
	"accepted": {},
	"archived": {},
}

var validTaskStates = map[string]struct{}{
	"todo":        {},
	"in_progress": {},
	"done":        {},
	"blocked":     {},
}

// Validate checks doc against the spec-format.yaml v1 schema and
// returns every issue it finds. doc may be nil (the result reports a
// single error).
//
// Validate is side-effect-free per spec-format.VAL.4.
func Validate(doc *Document, mode Mode) Result {
	v := &validator{mode: mode}
	if doc == nil {
		v.errf("", "internal", "document is nil")
		return v.result()
	}
	v.checkSpecVersion(doc)
	v.checkUnknownTopLevelKeys(doc)
	v.checkMetadata(doc)
	v.checkTasks(doc)
	v.checkComponents(doc)
	v.checkConstraints(doc)
	v.checkComponentConstraintCollision(doc)
	return v.result()
}

type validator struct {
	mode   Mode
	issues []Issue
}

func (v *validator) result() Result {
	return Result{Issues: v.issues}
}

func (v *validator) errf(path, category, format string, args ...any) {
	v.issues = append(v.issues, Issue{
		Path:     path,
		Category: category,
		Message:  fmt.Sprintf(format, args...),
		Severity: SeverityError,
	})
}

func (v *validator) warnf(path, category, format string, args ...any) {
	v.issues = append(v.issues, Issue{
		Path:     path,
		Category: category,
		Message:  fmt.Sprintf(format, args...),
		Severity: SeverityWarning,
	})
}

func (v *validator) checkSpecVersion(doc *Document) {
	switch doc.SpecVersion {
	case 0:
		v.errf("spec_version", "required-field", "spec_version is required")
	case 1:
		// ok
	default:
		v.errf("spec_version", "format", "spec_version %d is not understood by this validator (v1 only)", doc.SpecVersion)
	}
}

func (v *validator) checkUnknownTopLevelKeys(doc *Document) {
	for _, key := range doc.TopLevelKeys() {
		if _, ok := recognizedTopLevelKeys[key]; ok {
			continue
		}
		switch v.mode {
		case ModeStrict:
			v.errf(key, "unknown-key", "unrecognized top-level key %q (strict mode)", key)
		case ModeLenient:
			v.warnf(key, "unknown-key", "unrecognized top-level key %q (lenient mode)", key)
		}
	}
}

func (v *validator) checkMetadata(doc *Document) {
	m := doc.Metadata
	if m.ID == "" {
		v.errf("metadata.id", "required-field", "metadata.id is required")
	} else if !IsKebab(m.ID) {
		v.errf("metadata.id", "format", "metadata.id %q is not kebab-case", m.ID)
	}

	if m.Name == "" {
		v.errf("metadata.name", "required-field", "metadata.name is required")
	}

	if m.State == "" {
		v.errf("metadata.state", "required-field", "metadata.state is required")
	} else if _, ok := validMetadataStates[m.State]; !ok {
		v.errf("metadata.state", "format",
			"metadata.state %q is not one of draft, active, accepted, archived", m.State)
	}

	if m.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil {
			v.errf("metadata.created_at", "format",
				"metadata.created_at %q is not RFC3339: %v", m.CreatedAt, err)
		}
	}
	if m.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, m.UpdatedAt); err != nil {
			v.errf("metadata.updated_at", "format",
				"metadata.updated_at %q is not RFC3339: %v", m.UpdatedAt, err)
		}
	}
}

func (v *validator) checkTasks(doc *Document) {
	seen := make(map[string]int, len(doc.Tasks))
	for i, t := range doc.Tasks {
		base := fmt.Sprintf("tasks[%d]", i)
		if t.ID == "" {
			v.errf(base+".id", "required-field", "tasks[%d].id is required", i)
		} else if !IsKebab(t.ID) {
			v.errf(base+".id", "format", "tasks[%d].id %q is not kebab-case", i, t.ID)
		} else if prev, dup := seen[t.ID]; dup {
			v.errf(base+".id", "duplicate",
				"task id %q is duplicated (also at tasks[%d])", t.ID, prev)
		} else {
			seen[t.ID] = i
		}

		if t.Description == "" {
			v.errf(base+".description", "required-field",
				"tasks[%d].description is required", i)
		}

		if t.State == "" {
			v.errf(base+".state", "required-field", "tasks[%d].state is required", i)
		} else if _, ok := validTaskStates[t.State]; !ok {
			v.errf(base+".state", "format",
				"tasks[%d].state %q is not one of todo, in_progress, done, blocked", i, t.State)
		}

		for j, ref := range t.References {
			refPath := fmt.Sprintf("%s.references[%d]", base, j)
			if _, err := ParseACID(ref); err != nil {
				v.errf(refPath, "acid", "tasks[%d].references[%d] %q: %v", i, j, ref, err)
			}
		}
	}
}

func (v *validator) checkComponents(doc *Document) {
	for _, id := range doc.ComponentOrder() {
		base := "components." + id
		if !IsComponentID(id) {
			v.errf(base, "format",
				"component id %q does not match uppercase-with-hyphens shape (spec-format.COMP.1.1)", id)
		}
		c := doc.Components[id]
		if c.Name == "" {
			v.errf(base+".name", "required-field",
				"components.%s.name is required", id)
		}
		if len(c.Requirements) == 0 {
			v.errf(base+".requirements", "required-field",
				"components.%s.requirements is required and non-empty", id)
		}
		v.checkRequirementIDs(base, c.RequirementOrder())
	}
}

func (v *validator) checkConstraints(doc *Document) {
	for _, id := range doc.ConstraintOrder() {
		base := "constraints." + id
		if !IsComponentID(id) {
			v.errf(base, "format",
				"constraint group id %q does not match uppercase-with-hyphens shape (spec-format.COMP.1.1)", id)
		}
		c := doc.Constraints[id]
		if c.Description == "" {
			v.errf(base+".description", "required-field",
				"constraints.%s.description is required (spec-format.CONST.3)", id)
		}
		if len(c.Requirements) == 0 {
			v.errf(base+".requirements", "required-field",
				"constraints.%s.requirements is required and non-empty", id)
		}
		v.checkRequirementIDs(base, c.RequirementOrder())
	}
}

func (v *validator) checkRequirementIDs(base string, ids []string) {
	for _, id := range ids {
		if !IsRequirementID(id) {
			v.errf(base+".requirements."+id, "format",
				"requirement id %q does not match the documented shape (spec-format.REQ.1)", id)
		}
	}
}

func (v *validator) checkComponentConstraintCollision(doc *Document) {
	for _, cid := range doc.ConstraintOrder() {
		if _, clash := doc.Components[cid]; clash {
			v.errf("constraints."+cid, "collision",
				"id %q is used by both a component and a constraint group (spec-format.CONST.2)", cid)
		}
	}
}

// SortIssues sorts a slice of Issues by file, then path, then severity.
// The default Validate output preserves document walk order; callers
// aggregating across many specs typically want a stable sorted view.
func SortIssues(issues []Issue) []Issue {
	out := make([]Issue, len(issues))
	copy(out, issues)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Severity < out[j].Severity
	})
	return out
}
