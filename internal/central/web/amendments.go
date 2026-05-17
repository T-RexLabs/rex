package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/specamend"
	internalweb "github.com/asabla/rex/internal/web"
)

// centralAmendmentsProjection satisfies
// internalweb.AmendmentsProjection by walking the central
// GitStore scoped to one workspaceID. Proposed amendments live
// at `specs/_proposed/<stem>.yaml`; accepted ones at
// `specs/_proposed/_accepted/<stem>.yaml`. Both directories ride
// through the existing /sync/git replication, so the central
// sees the same content the local node pushed
// (storage.WS.2.2.1).
//
// Read-only: accept / reject mutations are local-only in v1
// because they involve identity signing and event-log writes
// that don't apply on central.
type centralAmendmentsProjection struct {
	store       GitEntityReader
	workspaceID string
	ctx         context.Context
}

func newCentralAmendmentsProjection(ctx context.Context, store GitEntityReader, workspaceID string) centralAmendmentsProjection {
	if ctx == nil {
		ctx = context.Background()
	}
	return centralAmendmentsProjection{store: store, workspaceID: workspaceID, ctx: ctx}
}

func (p centralAmendmentsProjection) ListAmendments(opts internalweb.AmendmentsListOptions) ([]internalweb.AmendmentRow, error) {
	if p.store == nil || p.workspaceID == "" {
		return nil, nil
	}
	paths, err := p.store.List(p.ctx, p.workspaceID)
	if err != nil {
		return nil, fmt.Errorf("central amendments: list: %w", err)
	}
	out := make([]internalweb.AmendmentRow, 0, len(paths))
	for _, path := range paths {
		state, ok := amendmentStateForPath(path)
		if !ok {
			continue
		}
		if opts.State != "" && opts.State != state {
			continue
		}
		stem := stemFromPath(path)
		rec, err := p.store.Get(p.ctx, p.workspaceID, path)
		if err != nil {
			continue
		}
		a, err := specamend.ParseAmendmentBytes(stem, []byte(rec.Content), state)
		if err != nil {
			continue
		}
		a.Path = path
		if opts.For != "" && !amendmentMatchesFor(a, opts.For) {
			continue
		}
		out = append(out, internalweb.NewAmendmentRow(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stem < out[j].Stem })
	return out, nil
}

func (p centralAmendmentsProjection) LoadAmendment(stem string, hl *internalweb.Highlighter) (internalweb.AmendmentDetail, bool, error) {
	if p.store == nil || p.workspaceID == "" {
		return internalweb.AmendmentDetail{}, false, nil
	}
	// Two candidate paths — proposed first, then accepted.
	candidates := []struct {
		path  string
		state specamend.State
	}{
		{"specs/_proposed/" + stem + ".yaml", specamend.StateProposed},
		{"specs/_proposed/_accepted/" + stem + ".yaml", specamend.StateAccepted},
	}
	for _, c := range candidates {
		rec, err := p.store.Get(p.ctx, p.workspaceID, c.path)
		if err != nil {
			continue
		}
		a, err := specamend.ParseAmendmentBytes(stem, []byte(rec.Content), c.state)
		if err != nil {
			return internalweb.AmendmentDetail{}, false, fmt.Errorf("central amendments: parse %s: %w", c.path, err)
		}
		a.Path = c.path
		return internalweb.NewAmendmentDetail(a, hl), true, nil
	}
	return internalweb.AmendmentDetail{}, false, nil
}

// amendmentStateForPath returns the lifecycle state implied by an
// amendment path under the GitStore. Returns (state, true) for
// recognised shapes; (_, false) for anything else (workspace.yaml,
// specs/foo.yaml, nested directories, etc.).
func amendmentStateForPath(path string) (specamend.State, bool) {
	const proposedPrefix = "specs/_proposed/"
	const acceptedPrefix = "specs/_proposed/_accepted/"
	if !strings.HasSuffix(path, ".yaml") {
		return "", false
	}
	switch {
	case strings.HasPrefix(path, acceptedPrefix):
		rest := strings.TrimPrefix(path, acceptedPrefix)
		if strings.Contains(rest, "/") {
			return "", false
		}
		return specamend.StateAccepted, true
	case strings.HasPrefix(path, proposedPrefix):
		rest := strings.TrimPrefix(path, proposedPrefix)
		if strings.Contains(rest, "/") {
			// _accepted/ + slug case is handled above; any other
			// nested layout is unrecognised.
			return "", false
		}
		return specamend.StateProposed, true
	}
	return "", false
}

// stemFromPath strips the directory + .yaml suffix to leave the
// amendment's stem (matching specamend's filename convention).
func stemFromPath(path string) string {
	idx := strings.LastIndexByte(path, '/')
	if idx >= 0 {
		path = path[idx+1:]
	}
	return strings.TrimSuffix(path, ".yaml")
}

// amendmentMatchesFor reports whether an amendment targets the
// given spec id via either the amendment_for field or, when
// target: multi, any of its changes[].affects entries (which we
// don't parse out here — multi amendments match everything in
// the For filter today). Cheap heuristic; the local shell uses
// specamend.List which does the same matching.
func amendmentMatchesFor(a *specamend.Amendment, for_ string) bool {
	if a.Multi {
		return true
	}
	return a.AmendmentFor == for_
}

// centralAmendmentsListData backs amendments_list.tmpl on the
// central shell. Mirrors the local envelope's field names so the
// shared template renders identically.
type centralAmendmentsListData struct {
	centralPageData
	Amendments     []internalweb.AmendmentRow
	StateFilter    string
	ForFilter      string
	AmendmentsBase string // form action + clear-link base URL
}

// centralAmendmentDetailData backs amendments_detail.tmpl on the
// central shell. Embeds the shared AmendmentDetail so the
// template's field paths resolve identically to the local
// envelope's embedded form. State is always non-proposed-or-empty
// on central so the accept/reject buttons render as absent
// (local-only mutation — see handleAmendmentsList for the
// read-only carve-out).
type centralAmendmentDetailData struct {
	centralPageData
	internalweb.AmendmentDetail
	Flash string
}

// handleAmendmentsList is GET /orgs/<org>/workspaces/<ws>/amendments.
func (s *Server) handleAmendmentsList(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	wsID := r.PathValue("ws")
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	if ws.Amendments == nil {
		http.Error(w, "central web: workspace has no amendments projection", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	stateFilter := q.Get("state")
	forFilter := q.Get("for")
	state, err := parseAmendmentStateFilter(stateFilter)
	if err != nil {
		http.Error(w, "central web: "+err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := ws.Amendments.ListAmendments(internalweb.AmendmentsListOptions{
		State: state,
		For:   forFilter,
	})
	if err != nil {
		http.Error(w, "central web: list amendments: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Stamp the LinkBase + AmendmentsBase so the shared list
	// template renders org-scoped URLs (form action +
	// per-row links). Empty LinkBase keeps local rendering
	// (/specs/<id>, /amendments/<stem>) unchanged.
	base := "/orgs/" + orgID + "/workspaces/" + wsID
	for i := range rows {
		rows[i].LinkBase = base
	}
	data := centralAmendmentsListData{
		centralPageData: s.pageData(orgID, wsID, "amendments"),
		Amendments:      rows,
		StateFilter:     stateFilter,
		ForFilter:       forFilter,
		AmendmentsBase:  base + "/amendments",
	}
	s.renderer.Render(w, r, "amendments_list.tmpl", data)
}

// handleAmendmentDetail is GET /orgs/<org>/workspaces/<ws>/amendments/<stem>.
// Renders the shared amendments_detail.tmpl with the central
// envelope. The template's accept/reject buttons gate on
// .State == "proposed"; central-side mutations are out of scope
// in v1, but the buttons would post to /amendments/<stem>/accept
// on the same origin if we did wire them — leaving them visible
// would mislead the user, so the read-only carve-out filters
// state to never look proposed-actionable on central.
func (s *Server) handleAmendmentDetail(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	if _, _, ok := s.requireOrgMember(w, r, orgID); !ok {
		return
	}
	wsID := r.PathValue("ws")
	stem := r.PathValue("stem")
	if stem == "" {
		http.NotFound(w, r)
		return
	}
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	if ws.Amendments == nil {
		http.Error(w, "central web: workspace has no amendments projection", http.StatusServiceUnavailable)
		return
	}
	detail, found, err := ws.Amendments.LoadAmendment(stem, s.highlighter)
	if err != nil {
		http.Error(w, "central web: load amendment: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	// Force the displayed state away from "proposed" so the
	// shared template's accept/reject buttons render as absent.
	// Central can still display the underlying file's content;
	// the actionable lifecycle is reserved for the local shell
	// where signing + event-log writes are available.
	if detail.State == specamend.StateProposed {
		detail.State = specamend.State("proposed (read-only on central)")
	}
	data := centralAmendmentDetailData{
		centralPageData: s.pageData(orgID, wsID, "amendments"),
		AmendmentDetail: detail,
	}
	s.renderer.Render(w, r, "amendments_detail.tmpl", data)
}

// parseAmendmentStateFilter mirrors the local helper but lives
// here so the central package doesn't import internal/local/web.
func parseAmendmentStateFilter(s string) (specamend.State, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "proposed":
		return specamend.StateProposed, nil
	case "accepted":
		return specamend.StateAccepted, nil
	default:
		return "", fmt.Errorf("invalid state %q (want proposed or accepted)", s)
	}
}
