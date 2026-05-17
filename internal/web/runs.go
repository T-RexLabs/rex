package web

import (
	"sort"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// RunRow is one row on the runs list page (web-ui.SHARED.1
// run_row partial). Lifted from the local shell so both shells'
// list handlers render against the same struct.
//
// Kind is "shell" or "harness"; derived during the fold so the
// list template can badge harness-driven runs differently.
//
// StartedAt / EndedAt / Duration are pre-formatted strings so the
// template is purely presentational — Go's html/template has no
// stable time formatter exposed to authors.
type RunRow struct {
	RunID      string
	Name       string
	Kind       string
	Status     runner.RunStatus
	StartedAt  string
	EndedAt    string
	Duration   string
	NodeEvents int
	// SpecRefs and FromTask are recorded on the run.started event
	// when the run was launched from a spec recipe
	// (execution.RUN.1.1). The list view uses them for filtering
	// and per-row badges.
	SpecRefs []string
	FromTask string
	// LinkBase is the URL prefix the row links to for /runs/<id>
	// and /specs/<id>#<task>. Empty on the local shell (links
	// resolve to /runs/<id> directly); set to
	// "/orgs/<org>/workspaces/<ws>" on the central shell so the
	// click-throughs land on the right org-scoped surface
	// instead of the local-only path.
	LinkBase string
}

// RunsListProjection is the read-side surface the shared /runs
// list handler queries. Local resolvers wrap a file-tail over
// `<Root>/.rex/events.log`; central resolvers wrap a read from
// the central event store (central-node.DB.1) per
// web-ui.CENTRAL-LAYOUT.2.
type RunsListProjection interface {
	// ListRuns returns every run's summary row, sorted
	// most-recent-started-first. Returns an empty slice (not an
	// error) when no events.log exists.
	ListRuns() ([]RunRow, error)
}

// RunDetail is the simplified per-run view both shells can
// produce. The local shell composes it into its rich detail data
// (frame view + permission view + SSE wiring); the central shell
// uses it directly with a minimal terminal-state-only template,
// because central does not have an in-flight event source in v1
// (web-ui amendment 2026-05-16, Decision B).
//
// Events are the raw eventlog records that mention RunID, in log
// order. Callers can pretty-print Payload via the Highlighter for
// the rendered timeline.
type RunDetail struct {
	RunID     string
	Name      string
	Kind      string // shell | harness
	Status    runner.RunStatus
	StartedAt string
	EndedAt   string
	Duration  string
	SpecRefs  []string
	FromTask  string
	Events    []RunEventBasic
	// Terminal is true when Status is completed / failed /
	// cancelled. The central run-detail template uses it to
	// decide whether to render the "live tail not available on
	// central in v1" notice (non-terminal runs).
	Terminal bool
}

// RunEventBasic is the smallest row shape the central run-detail
// timeline needs: id + timestamp + type + raw payload. The local
// shell's richer typed-frame + permission rendering remains
// local-only; this struct is the lowest common denominator both
// shells can populate from an eventlog.Record.
type RunEventBasic struct {
	ID        string
	Timestamp string
	Type      string
	Payload   []byte // raw JSON; templates pre-render via the Highlighter
}

// RunDetailProjection is the read-side surface the shared /runs/<id>
// handler queries for the terminal-state view. Local resolvers
// implement the rich detail flow elsewhere (frame view +
// permission UI + SSE) because their event source supports
// live-tail; central uses this projection for terminal-state-only
// rendering.
type RunDetailProjection interface {
	// GetRun returns the run's terminal-state summary and the
	// chronological list of events that mention it. found is
	// false when the id matches no events at all; the caller
	// surfaces a 404.
	GetRun(runID string) (detail RunDetail, found bool, err error)
}

// FoldRecordsToRunRows is the pure fold used by every
// RunsListProjection implementation. Pass it the chronological
// slice of records (file-tail for local, store.Since for central)
// and it returns the rendered RunRow slice sorted
// most-recent-started-first.
//
// Unknown event types are skipped silently (consistent with
// overview.SYS.3 — readers tolerate unknown record types). A
// known type that fails to decode bubbles back as a non-nil
// error.
func FoldRecordsToRunRows(records []eventlog.Record) ([]RunRow, error) {
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	by := map[string]*runner.RunSummary{}
	kinds := map[string]string{}

	for _, rec := range records {
		decoded, err := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if err == event.ErrSkipUnknownType { //nolint:errorlint // sentinel comparison
			continue
		}
		if err != nil {
			return nil, err
		}
		if hf, ok := decoded.(runner.HarnessFrameEvent); ok {
			kinds[hf.RunID] = "harness"
			continue
		}
		var probe runner.RunSummary
		if !probe.FoldEvent(decoded) {
			continue
		}
		s, ok := by[probe.RunID]
		if !ok {
			s = &runner.RunSummary{}
			by[probe.RunID] = s
		}
		s.FoldEvent(decoded)
	}

	out := make([]RunRow, 0, len(by))
	for _, s := range by {
		kind := kinds[s.RunID]
		if kind == "" {
			kind = "shell"
		}
		row := RunRow{
			RunID:      s.RunID,
			Name:       runner.FriendlyName(s.RunID),
			Kind:       kind,
			Status:     s.EffectiveStatus(),
			StartedAt:  s.StartedAt.UTC().Format(time.RFC3339),
			NodeEvents: s.NodeEvents,
			SpecRefs:   append([]string(nil), s.SpecRefs...),
			FromTask:   s.FromTask,
		}
		if !s.EndedAt.IsZero() {
			row.EndedAt = s.EndedAt.UTC().Format(time.RFC3339)
			row.Duration = s.EndedAt.Sub(s.StartedAt).Truncate(time.Millisecond).String()
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

// FoldRecordsToRunDetail is the pure fold for the terminal-state
// run-detail view. Walks records once, computes the run's
// RunSummary + collects every record whose decoded event mentions
// runID. found is false when no record references runID.
func FoldRecordsToRunDetail(records []eventlog.Record, runID string) (RunDetail, bool, error) {
	if runID == "" {
		return RunDetail{}, false, nil
	}
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	summary := &runner.RunSummary{}
	var events []RunEventBasic
	kind := "shell"

	for _, rec := range records {
		decoded, err := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if err == event.ErrSkipUnknownType { //nolint:errorlint // sentinel comparison
			continue
		}
		if err != nil {
			return RunDetail{}, false, err
		}
		if !runner.MatchesRun(decoded, runID) {
			continue
		}
		if _, ok := decoded.(runner.HarnessFrameEvent); ok {
			kind = "harness"
		}
		summary.FoldEvent(decoded)
		events = append(events, RunEventBasic{
			ID:        rec.ID,
			Timestamp: time.Unix(0, rec.Timestamp.Wall).UTC().Format(time.RFC3339Nano),
			Type:      rec.Type,
			Payload:   append([]byte(nil), rec.Payload...),
		})
	}

	if len(events) == 0 {
		return RunDetail{}, false, nil
	}

	d := RunDetail{
		RunID:     runID,
		Name:      runner.FriendlyName(runID),
		Kind:      kind,
		Status:    summary.EffectiveStatus(),
		StartedAt: summary.StartedAt.UTC().Format(time.RFC3339),
		SpecRefs:  append([]string(nil), summary.SpecRefs...),
		FromTask:  summary.FromTask,
		Events:    events,
	}
	if !summary.EndedAt.IsZero() {
		d.EndedAt = summary.EndedAt.UTC().Format(time.RFC3339)
		d.Duration = summary.EndedAt.Sub(summary.StartedAt).Truncate(time.Millisecond).String()
	}
	d.Terminal = isTerminalStatus(d.Status)
	return d, true, nil
}

// isTerminalStatus reports whether a run status is a final state
// (no further events expected). Centralised here so the central
// "live tail not available" notice in the template can rely on
// the same definition the list view uses.
func isTerminalStatus(s runner.RunStatus) bool {
	switch s {
	case runner.RunStatusCompleted, runner.RunStatusAborted, runner.RunStatusCancelled:
		return true
	default:
		return false
	}
}

// SplitRunsBySpec partitions rows that cite specID into per-task
// buckets + a "untasked" slice for spec_refs-only matches, plus
// a deterministic merged slice for the "all runs for this spec"
// view. Used by the spec detail page's runs tab on both shells
// (the local shell had its own version against the unaliased
// runRow; the central side had no implementation at all so the
// tab rendered empty even when matching runs existed).
//
// Each bucket is sorted most-recent-started first (RFC3339
// timestamps sort lexically). The merged slice mirrors the
// local shell's prior behaviour: walk byTask in sorted task-id
// order then append untasked.
func SplitRunsBySpec(rows []RunRow, specID string) (byTask map[string][]RunRow, untasked, all []RunRow) {
	byTask = make(map[string][]RunRow)
	prefix := specID + "."
	for _, r := range rows {
		if !MatchesSpecFilter(r, specID) {
			continue
		}
		if strings.HasPrefix(r.FromTask, prefix) {
			taskID := strings.TrimPrefix(r.FromTask, prefix)
			byTask[taskID] = append(byTask[taskID], r)
			continue
		}
		untasked = append(untasked, r)
	}
	for k, bucket := range byTask {
		sortRunsDesc(bucket)
		byTask[k] = bucket
	}
	sortRunsDesc(untasked)

	// Merged "all" slice for the runs tab. Walk byTask in
	// task-id order so the resulting list is deterministic.
	taskIDs := make([]string, 0, len(byTask))
	for id := range byTask {
		taskIDs = append(taskIDs, id)
	}
	sort.Strings(taskIDs)
	for _, id := range taskIDs {
		all = append(all, byTask[id]...)
	}
	all = append(all, untasked...)
	return byTask, untasked, all
}

func sortRunsDesc(rows []RunRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].StartedAt > rows[j].StartedAt
	})
}

// MatchesSpecFilter returns true when r is launched against the
// supplied filter token. The filter may be:
//   - a fully-qualified ACID (e.g. `sync.ORDER.3`) — matches when
//     the run's spec_refs contains it exactly
//   - a bare spec id (e.g. `sync`) — matches when any spec_ref is
//     prefixed by `<id>.` or the from_task is prefixed by `<id>.`
//
// Empty filter matches everything; callers filter at the handler
// boundary rather than always calling this.
func MatchesSpecFilter(r RunRow, filter string) bool {
	if filter == "" {
		return true
	}
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
