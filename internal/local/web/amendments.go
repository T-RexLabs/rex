package web

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/specamend"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// amendmentRow is the template-friendly view of one amendment in
// the /amendments index.
type amendmentRow struct {
	Stem          string
	Path          string
	State         specamend.State
	AmendmentFor  string
	AmendmentDate string
	AmendmentKind string
	Multi         bool
	SummaryFirst  string
}

// amendmentsListData backs amendments_list.tmpl.
type amendmentsListData struct {
	pageData
	Amendments  []amendmentRow
	StateFilter string
	ForFilter   string
}

// amendmentDetailData backs amendments_detail.tmpl.
type amendmentDetailData struct {
	pageData
	Stem          string
	Path          string
	State         specamend.State
	AmendmentFor  string
	AmendmentDate string
	AmendmentKind string
	Multi         bool
	Summary       string
	BodyPretty    template.HTML // chroma-highlighted YAML
	BodyRaw       string
	// Flash holds a one-shot status message rendered above the
	// detail (e.g. "amendment accepted"). Empty on a cold load.
	Flash string
}

// handleAmendmentsList renders GET /amendments.
func (s *Server) handleAmendmentsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stateFilter := q.Get("state")
	forFilter := q.Get("for")

	state, err := parseAmendmentState(stateFilter)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusBadRequest)
		return
	}
	amendments, err := specamend.List(s.opts.WorkspaceRoot, specamend.ListOptions{
		State: state,
		For:   forFilter,
	})
	if err != nil {
		http.Error(w, "web: list amendments: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]amendmentRow, 0, len(amendments))
	for _, a := range amendments {
		rows = append(rows, toAmendmentRow(a))
	}

	base := s.basePageData()
	base.NavSection = "amendments"
	s.render(w, r, "amendments_list.tmpl", amendmentsListData{
		pageData:    base,
		Amendments:  rows,
		StateFilter: stateFilter,
		ForFilter:   forFilter,
	})
}

// handleAmendmentDetail renders GET /amendments/{stem}.
func (s *Server) handleAmendmentDetail(w http.ResponseWriter, r *http.Request) {
	stem := r.PathValue("stem")
	if stem == "" {
		http.NotFound(w, r)
		return
	}
	flash := r.URL.Query().Get("flash")
	s.renderAmendmentDetail(w, r, stem, flash, http.StatusOK)
}

// renderAmendmentDetail is the shared body for the GET path and
// the post-action reload when accept/reject succeed without a
// redirect (only the rejected branch — accepted goes back to
// _accepted/<stem> via the same URL).
func (s *Server) renderAmendmentDetail(w http.ResponseWriter, r *http.Request, stem, flash string, status int) {
	a, err := specamend.Load(s.opts.WorkspaceRoot, stem)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "web: load amendment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	base := s.basePageData()
	base.NavSection = "amendments"
	d := amendmentDetailData{
		pageData:      base,
		Stem:          a.Stem,
		Path:          a.Path,
		State:         a.State,
		AmendmentFor:  a.AmendmentFor,
		AmendmentDate: a.AmendmentDate,
		AmendmentKind: a.AmendmentKind,
		Multi:         a.Multi,
		Summary:       a.Summary,
		BodyRaw:       string(a.Body),
		Flash:         flash,
	}
	if s.highlighter != nil {
		d.BodyPretty = s.highlighter.HighlightYAML(string(a.Body))
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	s.render(w, r, "amendments_detail.tmpl", d)
}

// handleAmendmentAccept is POST /amendments/{stem}/accept.
func (s *Server) handleAmendmentAccept(w http.ResponseWriter, r *http.Request) {
	stem := r.PathValue("stem")
	if stem == "" {
		http.NotFound(w, r)
		return
	}
	res, err := specamend.Accept(s.opts.WorkspaceRoot, stem)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.emitAmendmentEvent(audit.EventTypeSpecAmendmentAccepted, audit.SpecAmendmentEvent{
		Stem:          res.Stem,
		AmendmentFor:  res.AmendmentFor,
		AmendmentDate: res.AmendmentDate,
		FromPath:      res.FromPath,
		ToPath:        res.ToPath,
	}); err != nil {
		http.Error(w, "web: emit audit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/amendments/"+res.Stem+"?flash=accepted", http.StatusSeeOther)
}

// handleAmendmentReject is POST /amendments/{stem}/reject. After
// the file is deleted there is no detail to redirect to, so we
// land on the index with a flash query param.
func (s *Server) handleAmendmentReject(w http.ResponseWriter, r *http.Request) {
	stem := r.PathValue("stem")
	if stem == "" {
		http.NotFound(w, r)
		return
	}
	res, err := specamend.Reject(s.opts.WorkspaceRoot, stem)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.emitAmendmentEvent(audit.EventTypeSpecAmendmentRejected, audit.SpecAmendmentEvent{
		Stem:          res.Stem,
		AmendmentFor:  res.AmendmentFor,
		AmendmentDate: res.AmendmentDate,
		FromPath:      res.FromPath,
	}); err != nil {
		http.Error(w, "web: emit audit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/amendments?flash=rejected:"+res.Stem, http.StatusSeeOther)
}

// emitAmendmentEvent opens an event-log writer, stamps the
// workspace id onto the payload, and appends the event. Mirrors
// the CLI's emitAuditEvent helper but without the cobra command
// dependency.
func (s *Server) emitAmendmentEvent(eventType string, payload audit.SpecAmendmentEvent) error {
	ws, err := loadWorkspaceSummary(s.opts.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}
	storeDir, err := identity.DefaultStoreDir()
	if err != nil {
		return fmt.Errorf("identity store dir: %w", err)
	}
	signer, err := identity.EnsureDefaultStoreSigner(identity.NewStore(storeDir))
	if err != nil {
		return fmt.Errorf("identity signer: %w", err)
	}
	payload.WorkspaceID = ws.ID

	searchIdx, idxErr := search.Open(s.opts.WorkspaceRoot)
	var onAppend func(eventlog.Record)
	if idxErr == nil {
		defer searchIdx.Close()
		indexerCB := search.EventIndexer(searchIdx, func(error) {})
		onAppend = func(rec eventlog.Record) { indexerCB(rec) }
	}

	writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(s.opts.WorkspaceRoot, ".rex", "events.log"),
		WorkspaceID: ws.ID,
		Actor:       signer.Actor().String(),
		Sign:        identity.SignFunc(signer),
		OnAppend:    onAppend,
	})
	if err != nil {
		return fmt.Errorf("open events.log: %w", err)
	}
	defer writer.Close()
	if _, err := audit.NewAppender(writer).Append(eventType, payload); err != nil {
		return fmt.Errorf("emit %s: %w", eventType, err)
	}
	return nil
}

// parseAmendmentState normalises the ?state= query param. Empty
// means "no filter".
func parseAmendmentState(s string) (specamend.State, error) {
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

func toAmendmentRow(a *specamend.Amendment) amendmentRow {
	return amendmentRow{
		Stem:          a.Stem,
		Path:          a.Path,
		State:         a.State,
		AmendmentFor:  a.AmendmentFor,
		AmendmentDate: a.AmendmentDate,
		AmendmentKind: a.AmendmentKind,
		Multi:         a.Multi,
		SummaryFirst:  firstLineOf(a.Summary),
	}
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// loadAmendmentsForSpec returns rows for the amendments-panel on
// /specs/<id>. Used by handleSpecDetail. Errors are swallowed so
// a missing _proposed/ directory yields an empty panel rather
// than a 500.
func loadAmendmentsForSpec(workspaceRoot, specID string) []amendmentRow {
	amendments, err := specamend.List(workspaceRoot, specamend.ListOptions{For: specID})
	if err != nil {
		return nil
	}
	out := make([]amendmentRow, 0, len(amendments))
	for _, a := range amendments {
		out = append(out, toAmendmentRow(a))
	}
	return out
}
