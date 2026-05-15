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
	internalweb "github.com/asabla/rex/internal/web"
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
	Frame      *frameView
	// Compact + Summary drive the dim single-line variant the
	// template renders for low-signal events: run/node lifecycle,
	// meta-class harness frames (usage updates, session boilerplate).
	// The full-card branch is reserved for transcript content
	// (agent_text, tool_call, tool_result) and events that need
	// interaction (permission prompts).
	Compact bool
	Summary string
}

// frameView is the typed-render shape for harness.frame events.
// When Kind is non-empty the template renders a typed card
// (assistant text, tool call, tool result, …) instead of the raw
// JSON payload. The raw payload is still available behind a
// debug toggle (?debug=1).
//
// Kind values:
//
//	"agent_text"    — text from the model (groups consecutive
//	                  agent_message_chunk frames into one card)
//	"agent_thought" — chain-of-thought, when the harness emits it
//	"tool_call"     — start of a tool invocation
//	"tool_result"   — completion of a tool invocation
//	"plan"          — plan_change updates
//	"meta"          — session/new, session/prompt and other ACP
//	                  protocol-level frames; rendered as a one-
//	                  liner with the method name
type frameView struct {
	Kind       string
	Role       string
	Method     string
	Text       string
	ToolName   string
	ToolCallID string
	Subtitle   string // free-form qualifier shown next to ToolName (e.g. the skill ref for a Skill tool)
	ToolArgs   template.HTML
	ToolOutput string // human-readable content[] payload from tool_call_update
	Status     string
}

// permissionView is the per-row shape behind LIVE.3's permission
// prompt. The request fields come from the permission.requested
// payload; the resolution fields are filled in only after a
// matching .granted / .denied event has landed.
type permissionView struct {
	RequestID   string
	Tool        string
	Reason      string
	Args        template.HTML // chroma-highlighted JSON of the request args
	HasArgs     bool
	RequestedAt string
	Resolved    bool
	Decision    string // "granted" | "denied"
	Resolver    string
	Note        string
	ResolvedAt  string
}

// newRunEventRow builds a runEventRow with chroma-highlighted JSON
// when hl is non-nil, falling back to the escaped raw bytes
// otherwise (e.g. unit tests that don't construct a Server).
func newRunEventRow(rec eventlog.Record, hl *internalweb.Highlighter) runEventRow {
	row := runEventRow{
		ID:        rec.ID,
		Timestamp: time.Unix(0, rec.Timestamp.Wall).UTC().Format(time.RFC3339Nano),
		Type:      rec.Type,
	}
	if hl != nil {
		row.Payload = hl.HighlightJSON(rec.Payload)
	} else {
		row.Payload = template.HTML(html.EscapeString(internalweb.PrettyJSON(rec.Payload)))
	}
	return row
}

// runsListData backs runs_list.tmpl.
type runsListData struct {
	pageData
	Runs       []runRow
	SpecFilter string
}

// loadRunsListFiltered narrows the runs list to those whose
// run.started event recorded the given fully-qualified ACID, or
// whose `from_task` reference begins with the given spec id (so
// passing a bare spec id like `execution` matches every run launched
// from any of its tasks). Empty filter returns every run.
//
// Implements the spec-id-prefix half of execution.RUN.1.2:
// `/specs/<id>` shows runs whose spec_refs contain any ACID
// prefixed by `<id>.` plus runs launched from that spec.
func loadRunsListFiltered(opts Options, specFilter string) (runsListData, error) {
	base := newPageDataFromOpts(opts)
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	runs, err := loadRunRows(opts.WorkspaceRoot)
	if err != nil {
		return runsListData{}, err
	}
	if specFilter != "" {
		filtered := runs[:0]
		for _, r := range runs {
			if matchesSpecFilter(r, specFilter) {
				filtered = append(filtered, r)
			}
		}
		runs = filtered
	}
	return runsListData{pageData: base, Runs: runs, SpecFilter: specFilter}, nil
}

// matchesSpecFilter returns true when r is launched against the
// supplied filter token. The filter may be:
//   - a fully-qualified ACID (e.g. `sync.ORDER.3`) — matches when
//     the run's spec_refs contains it exactly
//   - a bare spec id (e.g. `sync`) — matches when any spec_ref is
//     prefixed by `<id>.` or the from_task is prefixed by `<id>.`
func matchesSpecFilter(r runRow, filter string) bool {
	for _, ref := range r.SpecRefs {
		if ref == filter {
			return true
		}
		if strings.HasPrefix(ref, filter+".") {
			return true
		}
	}
	if r.FromTask == filter {
		return true
	}
	if strings.HasPrefix(r.FromTask, filter+".") {
		return true
	}
	return false
}

// loadRunsByTaskID groups every run that cites specID into
// per-task buckets (keyed by the task id suffix of from_task)
// and an "untasked" bucket for runs that cite the spec via
// spec_refs without naming a task. Used by /specs/<id> to render
// the per-task run-history affordance.
//
// Within each bucket, runs are ordered most-recent-first so the
// UI's "latest run" indicator (status dot) reads off the head.
func loadRunsByTaskID(root, specID string) (map[string][]runRow, []runRow, error) {
	all, err := loadRunRows(root)
	if err != nil {
		return nil, nil, err
	}
	prefix := specID + "."
	byTask := make(map[string][]runRow)
	var untasked []runRow
	for _, r := range all {
		if !matchesSpecFilter(r, specID) {
			continue
		}
		if strings.HasPrefix(r.FromTask, prefix) {
			taskID := strings.TrimPrefix(r.FromTask, prefix)
			byTask[taskID] = append(byTask[taskID], r)
			continue
		}
		untasked = append(untasked, r)
	}
	// Most-recent-first per bucket. loadRunRows produces no
	// guaranteed order; we sort by started timestamp string
	// (RFC3339, lexically sortable).
	for k := range byTask {
		buck := byTask[k]
		sortRunsDesc(buck)
		byTask[k] = buck
	}
	sortRunsDesc(untasked)
	return byTask, untasked, nil
}

// sortRunsDesc orders runs most-recent-started first. Reverse-
// alphabetical works because StartedAt is RFC3339.
func sortRunsDesc(rows []runRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].StartedAt > rows[j-1].StartedAt; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
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
	RunID        string
	Name         string
	Status       runner.RunStatus
	Events       []runEventRow
	LastEventID  string
	AcceptsInput bool
	ActivePrompt *permissionView
	Debug        bool
	// OptimisticPrompt is the user's prompt as captured by /runs/start.
	// Rendered as a synthetic transcript row before the harness echoes
	// it back as a user_message_chunk, so the user sees their message
	// the moment they hit submit. JS removes it once the real user
	// message arrives.
	OptimisticPrompt string
	// HasUserMessage is true when the events log already contains a
	// user_message_chunk frame. The template uses it to decide whether
	// to render OptimisticPrompt — if the harness already echoed, the
	// synthetic row is redundant.
	HasUserMessage bool
	// SpecRefs are the fully-qualified ACIDs the run cited at
	// start (execution.RUN.1.1). Phase-C surfaces them as
	// clickable links back to the originating spec.
	SpecRefs []string
	// FromTask is the `<spec-id>.<task-id>` reference when the
	// run was launched from a recipe; empty otherwise. The
	// template renders it as a link to /specs/<spec-id> when set.
	FromTask string
}

// loadRunDetail walks events.log and returns the records whose
// decoded payload references runID. Found is false when the run
// id matches no events at all. hl is used to pre-render each
// payload as syntax-highlighted JSON.
func loadRunDetail(opts Options, runID string, hl *internalweb.Highlighter) (runDetailData, bool, error) {
	base := newPageDataFromOpts(opts)
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
		// harness.frame events get a typed view; consecutive
		// agent_text frames coalesce into one row so a model's
		// chunked turn doesn't bloat the timeline. The grouping
		// logic only activates when the frame parses cleanly —
		// if categorizeFrame returns nil we fall back to the
		// raw JSON view alongside every other event.
		if hf, ok := decoded.(runner.HarnessFrameEvent); ok {
			if fv := categorizeFrame(hf, hl); fv != nil {
				if (fv.Kind == "agent_text" || fv.Kind == "agent_thought") && len(collected) > 0 {
					prev := &collected[len(collected)-1]
					if pf := prev.row.Frame; pf != nil && pf.Kind == fv.Kind && pf.Role == fv.Role {
						pf.Text += fv.Text
						continue
					}
				}
				// Tool calls: every later tool_call_update for the
				// same toolCallId merges into the original card so
				// the timeline shows one row per logical invocation
				// instead of one per status transition.
				if fv.ToolCallID != "" && fv.Kind == "tool_result" {
					merged := false
					for i := len(collected) - 1; i >= 0; i-- {
						pf := collected[i].row.Frame
						if pf == nil || pf.ToolCallID != fv.ToolCallID {
							continue
						}
						if fv.ToolName != "(unnamed)" && fv.ToolName != "" {
							pf.ToolName = fv.ToolName
						}
						if fv.Subtitle != "" {
							pf.Subtitle = fv.Subtitle
						}
						if fv.ToolArgs != "" {
							pf.ToolArgs = fv.ToolArgs
						}
						if fv.ToolOutput != "" {
							pf.ToolOutput = fv.ToolOutput
						}
						pf.Status = fv.Status
						merged = true
						break
					}
					if merged {
						continue
					}
				}
				row.Frame = fv
				if fv.Kind == "meta" {
					row.Compact = true
				}
				if fv.Kind == "agent_text" && fv.Role == "user" {
					d.HasUserMessage = true
				}
			}
		}
		// Lifecycle events render as compact dim rows so they
		// don't compete with transcript content visually. Failed
		// nodes / aborted runs stay full cards so errors
		// stay visible. Permission events keep the full card so
		// the inline form has space to breathe.
		switch ev := decoded.(type) {
		case runner.RunStartedEvent:
			row.Compact, row.Summary = true, "run started"
			// Capture the spec linkage Phase-C renders in the
			// run-detail header. Earlier RunStartedEvents win
			// in the unlikely case of duplicates (replayed log).
			if d.FromTask == "" {
				d.FromTask = ev.FromTask
			}
			if len(d.SpecRefs) == 0 && len(ev.SpecRefs) > 0 {
				d.SpecRefs = append([]string{}, ev.SpecRefs...)
			}
		case runner.RunCompletedEvent:
			row.Compact, row.Summary = true, "run completed"
		case runner.RunCancelledEvent:
			row.Compact, row.Summary = true, "run cancelled"
		case runner.NodeStartedEvent:
			row.Compact, row.Summary = true, "node started · "+string(ev.NodeID)
		case runner.NodeSucceededEvent:
			row.Compact, row.Summary = true, "node succeeded · "+string(ev.NodeID)
		case runner.NodeRetriedEvent:
			row.Compact, row.Summary = true, "node retried · "+string(ev.NodeID)
		}
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
	var activePrompt *permissionView
	for _, c := range collected {
		row := c.row
		if req, ok := c.decoded.(runner.PermissionRequestedEvent); ok {
			perm := &permissionView{
				RequestID:   req.RequestID,
				Tool:        req.Tool,
				Reason:      req.Reason,
				RequestedAt: req.RequestedAt.UTC().Format(time.RFC3339Nano),
			}
			if len(req.Args) > 0 && string(req.Args) != "null" {
				perm.HasArgs = true
				if hl != nil {
					perm.Args = hl.HighlightJSON(req.Args)
				} else {
					perm.Args = template.HTML(html.EscapeString(internalweb.PrettyJSON(req.Args)))
				}
			}
			if r, ok := resolutions[req.RequestID]; ok {
				perm.Resolved = true
				perm.Decision = r.Decision
				perm.Resolver = r.Resolver
				perm.Note = r.Note
				perm.ResolvedAt = r.ResolvedAt
			} else {
				copy := *perm
				activePrompt = &copy
			}
			row.Permission = perm
		}
		d.Events = append(d.Events, row)
	}
	d.ActivePrompt = activePrompt

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
	debug := r.URL.Query().Get("debug") == "1"
	logPath := filepath.Join(s.opts.WorkspaceRoot, ".rex", "events.log")
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	seen := make(map[string]struct{})
	emit := func(rec eventlog.Record, decoded any) error {
		row := newRunEventRow(rec, s.highlighter)
		var body string

		// Lifecycle events render as compact dim rows (matches
		// the initial server render's Compact branch). Debug
		// mode short-circuits to the full raw card so operators
		// can see everything.
		if !debug {
			if summary, ok := lifecycleSummary(decoded); ok {
				body = renderCompactLifecycleHTML(row, rec.Type, summary, runID)
			}
		}

		// harness.frame events get the typed card; the JS shim
		// coalesces consecutive agent_text rows into one growing
		// turn (same logic the initial server render uses, just
		// applied DOM-side as new SSE frames arrive).
		if body == "" {
			if hf, ok := decoded.(runner.HarnessFrameEvent); ok {
				if fv := categorizeFrame(hf, s.highlighter); fv != nil {
					row.Frame = fv
					if fv.Kind == "meta" && !debug {
						body = renderCompactMetaHTML(row, fv)
					} else {
						body = renderFrameCardHTML(row, fv)
					}
				}
			}
		}
		if body == "" {
			body = fmt.Sprintf(
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
				string(row.Payload),
			)
		}
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
			decoded, derr := reg.Decode(event.Envelope{
				Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
			})
			if derr != nil {
				decoded = nil
			}
			if err := emit(rec, decoded); err != nil {
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
