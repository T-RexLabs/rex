package specfmt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ACIDRef is one parsed ACID reference. Short refers true when the
// original lacked a spec.id prefix; the validator/resolver supplies
// it from the enclosing spec's metadata.id (spec-format.ACID.1.1).
type ACIDRef struct {
	Original      string
	Short         bool
	SpecID        string
	Component     string
	RequirementID string
}

// String returns the canonical fully-qualified form. SpecID must be
// populated for the result to be a valid full ACID; the resolver fills
// it in for short refs before calling.
func (r ACIDRef) String() string {
	return fmt.Sprintf("%s.%s.%s", r.SpecID, r.Component, r.RequirementID)
}

var (
	kebabRE     = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	componentRE = regexp.MustCompile(`^[A-Z]+(-[A-Z]+)*$`)
	// reqIDRE matches the documented forms: numeric (`1`), dotted
	// numeric (`1.1`, `1.2.3`), and a trailing `-word` suffix
	// (`2-note`, `1.1-note`). Requirement IDs are case-sensitive but
	// the suffix is conventionally lowercase.
	reqIDRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*(-[a-z][a-z0-9-]*)?$`)
)

// IsKebab reports whether s is a valid kebab-case slug per
// spec-format.META.1 / TASK.2.
func IsKebab(s string) bool { return kebabRE.MatchString(s) }

// IsComponentID reports whether s is a valid uppercase-with-hyphens ID
// per spec-format.COMP.1.1.
func IsComponentID(s string) bool { return componentRE.MatchString(s) }

// IsRequirementID reports whether s matches the documented requirement
// ID shape per spec-format.REQ.1.
func IsRequirementID(s string) bool { return reqIDRE.MatchString(s) }

// ParseACID parses ref into an ACIDRef. It rejects inputs that do not
// match either the full or short form. Validation only: ParseACID does
// not check whether the target exists; that is the resolver's job.
//
// Disambiguation: requirement IDs themselves may contain dots
// (`1.1`, `1.2.3`), so naive segment-counting cannot distinguish a
// short-form `NAME.1.1` from a full-form `spec.NAME.1`. We discriminate
// on the first segment's case — uppercase ⇒ short, kebab ⇒ full.
func ParseACID(ref string) (ACIDRef, error) {
	if ref == "" {
		return ACIDRef{}, errors.New("acid: empty reference")
	}
	first, rest, ok := strings.Cut(ref, ".")
	if !ok {
		return ACIDRef{}, fmt.Errorf("acid: %q must have at least 2 dot-separated segments", ref)
	}
	switch {
	case IsComponentID(first):
		if !IsRequirementID(rest) {
			return ACIDRef{}, fmt.Errorf("acid: short form requirement id %q is malformed", rest)
		}
		return ACIDRef{
			Original:      ref,
			Short:         true,
			Component:     first,
			RequirementID: rest,
		}, nil
	case IsKebab(first):
		comp, req, ok := strings.Cut(rest, ".")
		if !ok {
			return ACIDRef{}, fmt.Errorf("acid: full form needs spec.COMPONENT.requirement, got %q", ref)
		}
		if !IsComponentID(comp) {
			return ACIDRef{}, fmt.Errorf("acid: component %q is not uppercase", comp)
		}
		if !IsRequirementID(req) {
			return ACIDRef{}, fmt.Errorf("acid: requirement id %q is malformed", req)
		}
		return ACIDRef{
			Original:      ref,
			SpecID:        first,
			Component:     comp,
			RequirementID: req,
		}, nil
	default:
		return ACIDRef{}, fmt.Errorf("acid: first segment %q is neither a component id (uppercase) nor a spec.id (kebab-case)", first)
	}
}
