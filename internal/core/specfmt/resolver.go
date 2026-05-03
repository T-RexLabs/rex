package specfmt

import (
	"errors"
	"fmt"
)

// Workspace is a registry of Documents the resolver searches over.
//
// A Workspace owns its membership: callers Add() each Document, then
// invoke Resolve / ValidateWorkspace. Membership is by metadata.id;
// adding two Documents with the same id is an error so the resolver
// always has a unique target for any reference.
type Workspace struct {
	docs     map[string]*Document
	docOrder []string
}

// NewWorkspace returns an empty workspace.
func NewWorkspace() *Workspace {
	return &Workspace{docs: make(map[string]*Document)}
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

// ValidateWorkspace runs Validate against every document in w and adds
// cross-spec ACID resolution checks: every parseable task reference
// must resolve to an existing requirement (spec-format.VAL.3). Issues
// from per-doc Validate runs carry the document's Path in Issue.File;
// dangling-ACID issues do the same.
//
// Mode follows Validate's strict/lenient convention. Lenient mode
// downgrades the dangling-ACID category to warnings since a missing
// cross-spec target may simply mean the workspace's spec set is
// incomplete relative to what's been authored.
func ValidateWorkspace(w *Workspace, mode Mode) Result {
	var issues []Issue
	for _, doc := range w.Specs() {
		// Per-doc structural validation.
		res := Validate(doc, mode)
		for _, issue := range res.Issues {
			issue.File = doc.Path
			issues = append(issues, issue)
		}

		// Cross-spec ACID resolution for every task reference. We
		// re-parse rather than relying on Validate's earlier parse
		// because Validate does not surface the parsed ACIDRef.
		for ti, task := range doc.Tasks {
			for ri, ref := range task.References {
				acid, err := ParseACID(ref)
				if err != nil {
					// Already reported by Validate; skip.
					continue
				}
				resolution := w.Resolve(acid, doc.Metadata.ID)
				if resolution.Found {
					continue
				}
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
			}
		}
	}
	return Result{Issues: issues}
}
