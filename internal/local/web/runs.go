package web

import (
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// Polling cadence for the SSE handler's file-tail loop. ~100ms is
// the budget the amendment commits us to and stays well within
// web-ui.PERF.1's 100ms TTFB target on localhost.
const ssePollInterval = 100 * time.Millisecond

// runEventRow flattens an eventlog.Record into a render-friendly
// shape for the run detail page. Payload is pre-formatted JSON
// HTML (chroma-highlighted on the server) so the template can
// drop it into a <pre> without further escaping.
//
// Permission is populated for permission.requested events only,
// so the template can render an inline approve/deny card next
// to the event payload (web-ui.LIVE.3). When the same run also
// emits a matching permission.granted or permission.denied
// event, Permission.Resolved is true and the buttons render as
// a quiet "resolved by X at Y" status row instead.
type runEventRow struct {
	ID         string
	Timestamp  string
	Type       string
	Payload    template.HTML
	Permission *permissionView
}

// permissionView is the per-row shape behind LIVE.3's permission
// prompt. The request fields come from the permission.requested
// payload; the resolution fields are filled in only after a
// matching .granted / .denied event has landed.
type permissionView struct {
	RequestID  string
	Tool       string
	Reason     string
	Resolved   bool
	Decision   string // "granted" | "denied"
	Resolver   string
	Note       string
	ResolvedAt string
}

// newRunEventRow builds a runEventRow with chroma-highlighted JSON
// when hl is non-nil, falling back to the escaped raw bytes
// otherwise (e.g. unit tests that don't construct a Server).
func newRunEventRow(rec eventlog.Record, hl *Highlighter) runEventRow {
	row := runEventRow{
		ID:        rec.ID,
		Timestamp: time.Unix(0, rec.Timestamp.Wall).UTC().Format(time.RFC3339Nano),
		Type:      rec.Type,
	}
	if hl != nil {
		row.Payload = hl.HighlightJSON(rec.Payload)
	} else {
		row.Payload = template.HTML(html.EscapeString(PrettyJSON(rec.Payload)))
	}
	return row
}

// runsListData backs runs_list.tmpl.
type runsListData struct {
	pageData
	Runs []runRow
}

func loadRunsList(opts Options) (runsListData, error) {
	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	runs, err := loadRunRows(opts.WorkspaceRoot)
	if err != nil {
		return runsListData{}, err
	}
	return runsListData{pageData: base, Runs: runs}, nil
}

// runDetailData backs run_detail.tmpl.
//
// LastEventID is the id of the chronologically last event the
// server rendered into the page. The template passes it to the
// SSE endpoint as ?after=<id> so the SSE handler does NOT replay
// events that are already in the page DOM — otherwise every page
// load shows each prior event twice (server-rendered + SSE
// replay).
type runDetailData struct {
	pageData
	RunID       string
	Name        string
	Status      runner.RunStatus
	Events      []runEventRow
	LastEventID string
}

// loadRunDetail walks events.log and returns the records whose
// decoded payload references runID. Found is false when the run
// id matches no events at all. hl is used to pre-render each
// payload as syntax-highlighted JSON.
func loadRunDetail(opts Options, runID string, hl *Highlighter) (runDetailData, bool, error) {
	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := runDetailData{pageData: base, RunID: runID, Name: runner.FriendlyName(runID)}
	logPath := filepath.Join(opts.WorkspaceRoot, ".rex", "events.log")
	r, err := eventlog.OpenReader(logPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return d, false, nil
		}
		return d, false, err
	}
	defer r.Close()

	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	// First pass: collect rows + permission resolutions keyed
	// by request_id. Doing this in two passes keeps the
	// decoration logic simple and matches the way the run
	// timeline reads chronologically.
	type rowDecode struct {
		row     runEventRow
		decoded any // typed runner.* payload
	}
	var collected []rowDecode
	resolutions := map[string]*permissionView{}

	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return d, false, err
		}
		decoded, derr := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if derr != nil {
			continue
		}
		if !runner.MatchesRun(decoded, runID) {
			continue
		}
		row := newRunEventRow(rec, hl)
		switch ev := decoded.(type) {
		case runner.PermissionGrantedEvent:
			resolutions[ev.RequestID] = &permissionView{
				Resolved:   true,
				Decision:   "granted",
				Resolver:   ev.Approver,
				Note:       ev.Note,
				ResolvedAt: ev.GrantedAt.UTC().Format(time.RFC3339Nano),
			}
		case runner.PermissionDeniedEvent:
			resolutions[ev.RequestID] = &permissionView{
				Resolved:   true,
				Decision:   "denied",
				Resolver:   ev.Approver,
				Note:       ev.Reason,
				ResolvedAt: ev.DeniedAt.UTC().Format(time.RFC3339Nano),
			}
		}
		collected = append(collected, rowDecode{row: row, decoded: decoded})

		// Track terminal status for the badge.
		switch rec.Type {
		case runner.EventTypeRunStarted:
			d.Status = runner.RunStatusRunning
		case runner.EventTypeRunCompleted:
			d.Status = runner.RunStatusCompleted
		case runner.EventTypeRunCancelled:
			d.Status = runner.RunStatusCancelled
		case runner.EventTypeRunAborted:
			d.Status = runner.RunStatusAborted
		}
	}

	// Second pass: decorate permission.requested rows with the
	// matching resolution (if any) so the template can render
	// either action buttons or a "resolved by X" status row.
	for _, c := range collected {
		row := c.row
		if req, ok := c.decoded.(runner.PermissionRequestedEvent); ok {
			perm := &permissionView{
				RequestID: req.RequestID,
				Tool:      req.Tool,
				Reason:    req.Reason,
			}
			if r, ok := resolutions[req.RequestID]; ok {
				perm.Resolved = true
				perm.Decision = r.Decision
				perm.Resolver = r.Resolver
				perm.Note = r.Note
				perm.ResolvedAt = r.ResolvedAt
			}
			row.Permission = perm
		}
		d.Events = append(d.Events, row)
	}

	if len(d.Events) == 0 {
		return d, false, nil
	}
	d.LastEventID = d.Events[len(d.Events)-1].ID
	return d, true, nil
}

// recordMatchesRun decodes rec via the runner registry and asks
// runner.MatchesRun whether the payload references runID. We
// deliberately decode (not substring-match the payload bytes) so a
// run id that happens to appear inside another event's payload
// doesn't false-positive.
func recordMatchesRun(reg *event.Registry, rec eventlog.Record, runID string) bool {
	decoded, err := reg.Decode(event.Envelope{
		Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
	})
	if err != nil {
		return false
	}
	return runner.MatchesRun(decoded, runID)
}

// streamRunEvents implements the SSE handler for /runs/<id>/stream.
//
// The handler honours an `after=<event-id>` query parameter: when
// set, it skips every event with id <= after (HLC strings sort
// lexicographically, matching their causal order), so the page's
// initial server-rendered events are NOT re-emitted as duplicates
// when the SSE connection opens. The run-detail template populates
// this from runDetailData.LastEventID.
//
// Without `after`, the handler replays every matching event from
// the start — useful for tools that read the stream raw without a
// pre-existing rendered page.
//
// After the initial scan, the handler tail-polls events.log at
// ssePollInterval, emitting each newly-appended matching record.
// The loop exits when the request context cancels (client
// disconnect) or, at the SSE protocol level, when the underlying
// TCP connection drops.
func (s *Server) streamRunEvents(w http.ResponseWriter, r *http.Request, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "web: streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	after := r.URL.Query().Get("after")
	logPath := filepath.Join(s.opts.WorkspaceRoot, ".rex", "events.log")
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	seen := make(map[string]struct{})
	emit := func(rec eventlog.Record) error {
		row := newRunEventRow(rec, s.highlighter)
		// Render the same timeline-card HTML the initial render
		// uses so the SSE-appended cards are visually identical
		// to the server-rendered ones.
		body := fmt.Sprintf(
			`<article class="event">`+
				`<header class="event-head">`+
				`<span class="event-type" data-type="%s"><code>%s</code></span>`+
				`<time class="event-time">%s</time>`+
				`<span class="event-id"><code>%s</code></span>`+
				`</header>`+
				`<pre class="event-body chroma"><code class="language-json">%s</code></pre>`+
				`</article>`,
			html.EscapeString(row.Type),
			html.EscapeString(row.Type),
			html.EscapeString(row.Timestamp),
			html.EscapeString(row.ID),
			string(row.Payload), // already-rendered safe HTML
		)
		// SSE multi-line data must prefix every line; flatten
		// embedded newlines so the wire frame is a single data: line.
		body = strings.ReplaceAll(body, "\n", " ")
		_, err := fmt.Fprintf(w, "event: run-event\ndata: %s\n\n", body)
		if err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	scan := func() error {
		f, err := eventlog.OpenReader(logPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		defer f.Close()
		for {
			rec, err := f.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if !recordMatchesRun(reg, rec, runID) {
				continue
			}
			// Skip events the page already rendered. Event IDs
			// are HLC strings; lexicographic <= matches causal
			// order, so anything <= `after` was on the page at
			// load time. Without this gate every page load
			// shows each prior event twice (server-rendered +
			// SSE replay).
			if after != "" && rec.ID <= after {
				continue
			}
			if _, dup := seen[rec.ID]; dup {
				continue
			}
			seen[rec.ID] = struct{}{}
			if err := emit(rec); err != nil {
				return err
			}
		}
	}

	// Initial replay.
	if err := scan(); err != nil {
		// Connection's already open; we can't return an HTTP
		// error code now. Comment out a SSE comment frame
		// and bail.
		fmt.Fprintf(w, ": initial-scan error %s\n\n", err.Error())
		flusher.Flush()
		return
	}

	// Tail-poll loop until context cancels.
	ticker := time.NewTicker(ssePollInterval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			if err := scan(); err != nil {
				return
			}
		}
	}
}

