package specfmt

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Workspace is a registry of Documents the resolver searches over.
//
// A Workspace owns its membership: callers Add() each Document, then
// invoke Resolve / ValidateWorkspace. Membership is by metadata.id;
// adding two Documents with the same id is an error so the resolver
// always has a unique target for any reference.
type Workspace struct {
	docs              map[string]*Document
	docOrder          []string
	defaultTemplateID string
}

// NewWorkspace returns an empty workspace.
func NewWorkspace() *Workspace {
	return &Workspace{docs: make(map[string]*Document)}
}

// SetDefaultTemplateID records the workspace's default template id
// (typically extra.default_template_id from workspace.yaml).
// ValidateWorkspace's second pass falls back to this id when a
// non-template spec does not opt into one explicitly via
// extra.template_id (spec-format.TMPL.4).
func (w *Workspace) SetDefaultTemplateID(id string) {
	w.defaultTemplateID = id
}

// DefaultTemplateID returns the previously-set default template id.
func (w *Workspace) DefaultTemplateID() string {
	return w.defaultTemplateID
}

// Add registers doc with the workspace. Returns an error if the
// metadata.id is empty or already taken.
func (w *Workspace) Add(doc *Document) error {
	if doc == nil {
		return errors.New("specfmt: Add nil doc")
	}
	if doc.Metadata.ID == "" {
		return errors.New("specfmt: cannot add doc with empty metadata.id")
	}
	if _, exists := w.docs[doc.Metadata.ID]; exists {
		return fmt.Errorf("specfmt: workspace already contains doc with id %q", doc.Metadata.ID)
	}
	w.docs[doc.Metadata.ID] = doc
	w.docOrder = append(w.docOrder, doc.Metadata.ID)
	return nil
}

// Specs returns the workspace's documents in insertion order.
func (w *Workspace) Specs() []*Document {
	out := make([]*Document, 0, len(w.docOrder))
	for _, id := range w.docOrder {
		out = append(out, w.docs[id])
	}
	return out
}

// Get returns the document with the given metadata.id and a flag
// indicating whether it was found.
func (w *Workspace) Get(id string) (*Document, bool) {
	doc, ok := w.docs[id]
	return doc, ok
}

// Resolution is what Resolve returns. Found is the truth bit; FullACID
// is what callers should report (regardless of Found) since the
// resolved spec.id is sometimes useful for "did you mean ...?" UX.
type Resolution struct {
	Original    ACIDRef
	FullACID    string
	Found       bool
	Doc         *Document
	InComponent bool // true: components; false: constraints
	GroupID     string
	Requirement Requirement
}

// Resolve expands ref against fromSpecID for short-form references and
// looks up the target requirement. fromSpecID is ignored when ref is
// already fully-qualified.
//
// A short-form ref with an empty fromSpecID is reported as not-found
// rather than being silently looked up against the empty spec id.
func (w *Workspace) Resolve(ref ACIDRef, fromSpecID string) Resolution {
	if ref.Short {
		ref.SpecID = fromSpecID
	}
	res := Resolution{
		Original: ref,
		FullACID: ref.String(),
	}
	if ref.SpecID == "" {
		return res
	}
	doc, ok := w.docs[ref.SpecID]
	if !ok {
		return res
	}
	if comp, ok := doc.Components[ref.Component]; ok {
		if r, ok := comp.Requirements[ref.RequirementID]; ok {
			res.Found = true
			res.Doc = doc
			res.InComponent = true
			res.GroupID = ref.Component
			res.Requirement = r
			return res
		}
	}
	if cons, ok := doc.Constraints[ref.Component]; ok {
		if r, ok := cons.Requirements[ref.RequirementID]; ok {
			res.Found = true
			res.Doc = doc
			res.InComponent = false
			res.GroupID = ref.Component
			res.Requirement = r
			return res
		}
	}
	return res
}

// ValidateWorkspace runs the v1 schema validator against every
// document in w (first pass) and adds cross-spec ACID resolution
// (spec-format.VAL.3). When templates are present, a second pass
// per spec-format.TMPL.3 checks each non-template spec against its
// active template's required_extra keys.
//
// Mode follows Validate's strict/lenient convention. Lenient mode
// downgrades dangling-ACID and template-required-extra issues to
// warnings since a missing cross-spec target or convention slip
// often reflects an in-progress authoring pass.
func ValidateWorkspace(w *Workspace, mode Mode) Result {
	var issues []Issue
	for _, doc := range w.Specs() {
		// Per-doc structural validation (first pass).
		res := Validate(doc, mode)
		for _, issue := range res.Issues {
			issue.File = doc.Path
			issues = append(issues, issue)
		}

		// Filename ↔ metadata.id check (spec-format.VAL.6). Files
		// inside specs/_proposed/ (and its _accepted/ subdir) are
		// exempt — those are amendment proposals, not specs.
		if iss := checkFilenameMatch(doc, mode); iss != nil {
			issues = append(issues, *iss)
		}

		// metadata.related_specs dangling-id warning (spec-format.META.7).
		for ri, related := range doc.Metadata.RelatedSpecs {
			if _, ok := w.Get(related); ok {
				continue
			}
			issues = append(issues, Issue{
				File:     doc.Path,
				Path:     fmt.Sprintf("metadata.related_specs[%d]", ri),
				Category: "dangling-related-spec",
				Message: fmt.Sprintf(
					"related_specs[%d] %q does not match any spec in this workspace",
					ri, related),
				Severity: SeverityWarning,
			})
		}

		// Cross-spec ACID resolution for every task reference. We
		// re-parse rather than relying on Validate's earlier parse
		// because Validate does not surface the parsed ACIDRef.
		// VAL.9 piggybacks on the resolved Requirement so we know
		// whether a referenced requirement is deprecated.
		for ti, task := range doc.Tasks {
			for ri, ref := range task.References {
				acid, err := ParseACID(ref)
				if err != nil {
					// Already reported by Validate; skip.
					continue
				}
				resolution := w.Resolve(acid, doc.Metadata.ID)
				if !resolution.Found {
					severity := SeverityError
					if mode == ModeLenient {
						severity = SeverityWarning
					}
					issues = append(issues, Issue{
						File:     doc.Path,
						Path:     fmt.Sprintf("tasks[%d].references[%d]", ti, ri),
						Category: "dangling-acid",
						Message: fmt.Sprintf(
							"reference %q resolves to %s but no such requirement exists",
							ref, resolution.FullACID),
						Severity: severity,
					})
					continue
				}
				if resolution.Requirement.Deprecated {
					replaced := ""
					if resolution.Requirement.ReplacedBy != "" {
						replaced = fmt.Sprintf(" (see replaced_by: %s)", resolution.Requirement.ReplacedBy)
					}
					issues = append(issues, Issue{
						File:     doc.Path,
						Path:     fmt.Sprintf("tasks[%d].references[%d]", ti, ri),
						Category: "deprecated-reference",
						Message: fmt.Sprintf(
							"reference %q points at a deprecated requirement%s (spec-format.VAL.9)",
							ref, replaced),
						Severity: SeverityWarning,
					})
				}
			}
		}

		// Second pass: template conformance (spec-format.TMPL.3).
		if !IsTemplate(doc) {
			template := activeTemplateFor(w, doc)
			if template != nil {
				issues = append(issues, validateAgainstTemplate(doc, template, mode)...)
			}
		}
	}
	return Result{Issues: issues}
}

// checkFilenameMatch enforces spec-format.VAL.6: a spec's filename
// must match its metadata.id. The check fires only when the doc
// lives in a directory named `specs/` (the canonical home of a
// workspace's spec set). One-off files passed to `rex spec
// validate ./somefile.yaml` and the package's test fixtures are
// exempt; amendment files under `specs/_proposed/` are exempt
// too because they are amendment proposals, not specs.
func checkFilenameMatch(doc *Document, mode Mode) *Issue {
	if doc.Path == "" || doc.Metadata.ID == "" {
		return nil
	}
	slash := filepath.ToSlash(doc.Path)
	if strings.Contains(slash, "/_proposed/") {
		return nil
	}
	parent := filepath.Base(filepath.Dir(doc.Path))
	if parent != "specs" {
		return nil
	}
	expected := doc.Metadata.ID + ".yaml"
	got := filepath.Base(doc.Path)
	if got == expected {
		return nil
	}
	severity := SeverityError
	if mode == ModeLenient {
		severity = SeverityWarning
	}
	return &Issue{
		File:     doc.Path,
		Path:     "metadata.id",
		Category: "filename-mismatch",
		Message: fmt.Sprintf(
			"filename %q does not match metadata.id %q (expected %s) (spec-format.VAL.6)",
			got, doc.Metadata.ID, expected),
		Severity: severity,
	}
}

// activeTemplateFor selects the template that governs doc per
// spec-format.TMPL.4 precedence: explicit extra.template_id →
// workspace's default → none. Returns nil when no template applies
// or the resolved template is missing.
func activeTemplateFor(w *Workspace, doc *Document) *Document {
	id := templateID(doc)
	if id == "" {
		id = w.DefaultTemplateID()
	}
	if id == "" {
		return nil
	}
	return w.Template(id)
}

// validateAgainstTemplate runs the second-pass conformance check.
// v1 enforces required_extra: every key listed in the template's
// extra.required_extra must be present in doc.Extra (any value,
// including empty, satisfies presence — surfacing missing keys is
// the value, not enforcing non-empty values).
func validateAgainstTemplate(doc, template *Document, mode Mode) []Issue {
	severity := SeverityError
	if mode == ModeLenient {
		severity = SeverityWarning
	}
	required := requiredExtraKeys(template)
	if len(required) == 0 {
		return nil
	}
	var out []Issue
	for _, key := range required {
		if _, ok := doc.Extra[key]; !ok {
			out = append(out, Issue{
				File:     doc.Path,
				Path:     "extra." + key,
				Category: "template-required-extra",
				Message: fmt.Sprintf(
					"template %q requires extra.%s but it is missing",
					template.Metadata.ID, key),
				Severity: severity,
			})
		}
	}
	return out
}
