package web

import (
	"html/template"
	"strings"

	"github.com/asabla/rex/internal/core/specamend"
)

// AmendmentRow is one row in the /amendments index list. Mirrors
// the local shell's prior shape so the existing shared partial
// (and the local amendments_list.tmpl) renders unchanged.
type AmendmentRow struct {
	Stem          string
	Path          string
	State         specamend.State
	AmendmentFor  string
	AmendmentDate string
	AmendmentKind string
	Multi         bool
	SummaryFirst  string
	// LinkBase is the URL prefix the row's stem-link uses for
	// /amendments/<stem> and the for-spec link for /specs/<id>.
	// Empty on the local shell; set to
	// "/orgs/<org>/workspaces/<ws>" on the central shell so the
	// click-through resolves to the workspace-scoped amendment +
	// spec detail pages.
	LinkBase string
}

// AmendmentDetail is the per-amendment payload the detail page
// renders. Includes the chroma-pretty-printed body so the source
// tab can drop it straight into the template.
type AmendmentDetail struct {
	Stem          string
	Path          string
	State         specamend.State
	AmendmentFor  string
	AmendmentDate string
	AmendmentKind string
	Multi         bool
	Summary       string
	BodyRaw       string
	BodyPretty    template.HTML
}

// AmendmentsListOptions filters the /amendments index. Empty
// fields mean "no filter on this dimension"; mirrors
// specamend.ListOptions so local resolvers can pass it straight
// through.
type AmendmentsListOptions struct {
	State specamend.State
	For   string
}

// AmendmentsProjection is the read-side surface the shared
// /amendments handlers query. Local resolvers wrap specamend's
// filesystem-backed list/load; central resolvers walk the
// GitStore for `specs/_proposed/*.yaml` entries and parse via
// specamend.ParseAmendmentBytes (web-ui.CENTRAL-LAYOUT.2).
//
// Note this projection is read-only. Accept / reject mutations
// remain local-only — they involve identity signing and event-log
// writes that don't apply on central in v1.
type AmendmentsProjection interface {
	// ListAmendments returns rows matching opts, sorted by the
	// underlying implementation's natural order (specamend orders
	// by file name; central implementations should match).
	ListAmendments(opts AmendmentsListOptions) ([]AmendmentRow, error)

	// LoadAmendment returns the per-file detail for stem. found
	// is false when no `_proposed/<stem>.yaml` or
	// `_proposed/_accepted/<stem>.yaml` entry exists. hl, when
	// non-nil, populates BodyPretty with chroma-highlighted YAML.
	LoadAmendment(stem string, hl *Highlighter) (AmendmentDetail, bool, error)
}

// NewAmendmentRow projects a parsed specamend.Amendment into the
// list-row shape. Lifted from the local shell so both resolvers
// produce identical rows.
func NewAmendmentRow(a *specamend.Amendment) AmendmentRow {
	return AmendmentRow{
		Stem:          a.Stem,
		Path:          a.Path,
		State:         a.State,
		AmendmentFor:  a.AmendmentFor,
		AmendmentDate: a.AmendmentDate,
		AmendmentKind: a.AmendmentKind,
		Multi:         a.Multi,
		SummaryFirst:  FirstLineOf(a.Summary),
	}
}

// NewAmendmentDetail projects a parsed specamend.Amendment into
// the detail-page shape, optionally pre-rendering BodyPretty when
// hl is non-nil. Body bytes ride through as-is.
func NewAmendmentDetail(a *specamend.Amendment, hl *Highlighter) AmendmentDetail {
	d := AmendmentDetail{
		Stem:          a.Stem,
		Path:          a.Path,
		State:         a.State,
		AmendmentFor:  a.AmendmentFor,
		AmendmentDate: a.AmendmentDate,
		AmendmentKind: a.AmendmentKind,
		Multi:         a.Multi,
		Summary:       a.Summary,
		BodyRaw:       string(a.Body),
	}
	if hl != nil {
		d.BodyPretty = hl.HighlightYAML(string(a.Body))
	}
	return d
}

// FirstLineOf returns the first non-blank line of s, with leading
// + trailing whitespace stripped. Used by amendment row summaries
// to fit a long body into one table cell.
func FirstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
