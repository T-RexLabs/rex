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
// drop it into a <pre> without further escaping. PayloadRaw is the
// pretty-printed source string used by the JS-disabled fallback —
// when JS is off the highlighted version still works (it's class
// based + server rendered) but the fallback is kept for SSE wire
// emission where we send pretty text only.
type runEventRow struct {
	ID        string
	Timestamp string
	Type      string
	Payload   template.HTML
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
type runDetailData struct {
	pageData
	RunID  string
	Status runner.RunStatus
	Events []runEventRow
}

// loadRunDetail walks events.log and returns the records whose
// decoded payload references runID. Found is false when the run
// id matches no events at all. hl is used to pre-render each
// payload as syntax-highlighted JSON.
func loadRunDetail(opts Options, runID string, hl *Highlighter) (runDetailData, bool, error) {
	base := pageData{BindAddr: opts.BindAddr, Version: opts.Version}
	ws, _ := loadWorkspaceSummary(opts.WorkspaceRoot)
	base.Workspace = ws

	d := runDetailData{pageData: base, RunID: runID}
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

	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return d, false, err
		}
		if !recordMatchesRun(reg, rec, runID) {
			continue
		}
		d.Events = append(d.Events, newRunEventRow(rec, hl))
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
	if len(d.Events) == 0 {
		return d, false, nil
	}
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
// On connect: replay every prior event for the run. Then poll
// events.log at ssePollInterval, emitting each newly-appended
// matching record. The loop exits when the request context cancels
// (client disconnect) or, at the SSE protocol level, when the
// underlying TCP connection drops.
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

