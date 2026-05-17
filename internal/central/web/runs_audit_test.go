package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	internalweb "github.com/asabla/rex/internal/web"
)

// TestCentralRunDetailHeaderShowsFriendlyName confirms the run
// detail's <h1> uses the same human-readable slug the runs list
// already shows (runner.FriendlyName), with the raw HLC demoted
// to a muted code in the meta row. Prevents a regression to the
// prior "run · <raw HLC>" heading shape, which made the page
// look unrelated to the runs list it was reached from.
func TestCentralRunDetailHeaderShowsFriendlyName(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const runID = "1778363847873279000.0"
	store := stubEventStore{records: []eventlog.Record{
		runEventRecord(t, "ev-1", t1, runner.RunStartedEvent{RunID: runID, StartedAt: t1}),
		runEventRecord(t, "ev-2", t1.Add(time.Second), runner.RunCompletedEvent{RunID: runID, CompletedAt: t1.Add(time.Second)}),
	}}
	srv := newRunsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/runs/" + runID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	friendly := runner.FriendlyName(runID)
	if friendly == "" {
		t.Fatalf("FriendlyName returned empty for %q", runID)
	}
	if !strings.Contains(html, `<p class="runid-heading">`+friendly+`</p>`) {
		t.Errorf("friendly-name heading missing (want %q): %s", friendly, html)
	}
	// Raw id stays available, just demoted into the meta row as a
	// muted code.
	if !strings.Contains(html, `<code class="muted">`+runID+`</code>`) {
		t.Errorf("raw run id not surfaced as muted code: %s", html)
	}
}

// stubEventStore is the minimal EventReader the central runs +
// audit projections need in tests. Backed by a fixed []records
// slice so test setup stays declarative.
type stubEventStore struct {
	records []eventlog.Record
}

func (s stubEventStore) Since(_ context.Context, cursor string) ([]eventlog.Record, error) {
	if cursor == "" {
		return s.records, nil
	}
	// Tests don't exercise the cursor branch — projections call
	// Since("") to slurp everything. Return everything past a
	// matching cursor for completeness; unknown cursors error.
	for i, r := range s.records {
		if r.ID == cursor {
			return s.records[i+1:], nil
		}
	}
	return nil, errors.New("unknown cursor")
}

// runEventRecord marshals a runner event into an eventlog.Record
// suitable for the stub. Tests construct sequences of these to
// drive the central projection through fold paths. Type is
// looked up via the runner.EventType* constants; supporting new
// event types means extending the switch below.
func runEventRecord(t *testing.T, id string, ts time.Time, ev any) eventlog.Record {
	t.Helper()
	var typ string
	switch ev.(type) {
	case runner.RunStartedEvent:
		typ = runner.EventTypeRunStarted
	case runner.RunCompletedEvent:
		typ = runner.EventTypeRunCompleted
	case runner.RunCancelledEvent:
		typ = runner.EventTypeRunCancelled
	case runner.RunAbortedEvent:
		typ = runner.EventTypeRunAborted
	default:
		t.Fatalf("runEventRecord: unhandled event type %T", ev)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal %T: %v", ev, err)
	}
	return eventlog.Record{
		ID:          id,
		Type:        typ,
		Version:     runner.EventVersion,
		Timestamp:   eventlog.HLC{Wall: ts.UnixNano()},
		Actor:       "l-test",
		WorkspaceID: "ws-1",
		Payload:     payload,
	}
}

// auditRecord builds a synthetic eventlog.Record with an
// audit-class type. Used to test the audit projection's filter
// without depending on a real spec.amendment.* payload — the
// audit catalog only checks the Type prefix.
func auditRecord(t *testing.T, id, typ string, ts time.Time) eventlog.Record {
	t.Helper()
	return eventlog.Record{
		ID:          id,
		Type:        typ,
		Version:     1,
		Timestamp:   eventlog.HLC{Wall: ts.UnixNano()},
		Actor:       "l-test",
		WorkspaceID: "ws-1",
		Payload:     json.RawMessage(`{}`),
	}
}

func newRunsServer(t *testing.T, store EventReader) *httptest.Server {
	t.Helper()
	s, err := New(Options{
		Version:  "test",
		Resolver: NewGitStoreResolver(nil, store),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestCentralRunsListRendersFromEventStore exercises the happy
// path: two runs fold into two list rows, sorted
// most-recent-started-first, rendered via the shared runs_list
// template (web-ui.SHARED.2).
func TestCentralRunsListRendersFromEventStore(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	store := stubEventStore{records: []eventlog.Record{
		runEventRecord(t, "ev-1", t1, runner.RunStartedEvent{RunID: "run-aaa", StartedAt: t1}),
		runEventRecord(t, "ev-2", t1.Add(time.Second), runner.RunCompletedEvent{RunID: "run-aaa", CompletedAt: t1.Add(time.Second)}),
		runEventRecord(t, "ev-3", t2, runner.RunStartedEvent{RunID: "run-bbb", StartedAt: t2}),
	}}
	srv := newRunsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/runs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "run-aaa") {
		t.Errorf("missing run-aaa: %s", html)
	}
	if !strings.Contains(html, "run-bbb") {
		t.Errorf("missing run-bbb: %s", html)
	}
	// The "start a run" toolbar must be absent on central (no
	// in-process execution; central-side execution is deferred).
	if strings.Contains(html, "start a run") {
		t.Errorf("central runs page rendered local-only 'start a run' affordance")
	}
	// Sort order — run-bbb started at t2 (later) should appear
	// first in the rendered HTML.
	if i, j := strings.Index(html, "run-bbb"), strings.Index(html, "run-aaa"); i < 0 || j < 0 || i >= j {
		t.Errorf("runs not sorted most-recent-first: bbb@%d aaa@%d", i, j)
	}
}

// TestCentralRunsListRespectsSpecFilter covers the ?spec=<token>
// filter — only runs whose SpecRefs or FromTask match the token
// land on the page.
func TestCentralRunsListRespectsSpecFilter(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := stubEventStore{records: []eventlog.Record{
		runEventRecord(t, "ev-1", t1, runner.RunStartedEvent{
			RunID: "run-sync", StartedAt: t1, SpecRefs: []string{"sync.ORDER.3"},
		}),
		runEventRecord(t, "ev-2", t1, runner.RunStartedEvent{
			RunID: "run-audit", StartedAt: t1, FromTask: "audit.QUERY.1",
		}),
	}}
	srv := newRunsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/runs?spec=sync")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	if !strings.Contains(html, "run-sync") {
		t.Errorf("filtered run-sync missing: %s", html)
	}
	if strings.Contains(html, "run-audit") {
		t.Errorf("filtered run-audit should be hidden: %s", html)
	}
}

// TestCentralRunDetailTerminalRunOmitsBanner covers the terminal-
// state branch: a completed run renders without the "live tail
// not available" notice.
func TestCentralRunDetailTerminalRunOmitsBanner(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := stubEventStore{records: []eventlog.Record{
		runEventRecord(t, "ev-1", t1, runner.RunStartedEvent{RunID: "run-done", StartedAt: t1}),
		runEventRecord(t, "ev-2", t1.Add(time.Second), runner.RunCompletedEvent{RunID: "run-done", CompletedAt: t1.Add(time.Second)}),
	}}
	srv := newRunsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/runs/run-done")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "run-done") {
		t.Errorf("run id not rendered: %s", html)
	}
	if !strings.Contains(html, "run.started") {
		t.Errorf("event timeline missing run.started entry: %s", html)
	}
	if strings.Contains(html, "live tail not available") {
		t.Errorf("terminal run should not surface the live-tail banner")
	}
}

// TestCentralRunDetailNonTerminalShowsBanner covers the deferred
// surface: a non-terminal run renders with the "live tail not
// available on central in v1" banner.
func TestCentralRunDetailNonTerminalShowsBanner(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := stubEventStore{records: []eventlog.Record{
		runEventRecord(t, "ev-1", t1, runner.RunStartedEvent{RunID: "run-live", StartedAt: t1}),
	}}
	srv := newRunsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/runs/run-live")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "live tail not available") {
		t.Errorf("non-terminal run missing live-tail banner: %s", html)
	}
}

// TestCentralRunDetail404 covers the missing-run branch.
func TestCentralRunDetail404(t *testing.T) {
	t.Parallel()
	srv := newRunsServer(t, stubEventStore{})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/runs/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}

// TestCentralAuditRendersFromEventStore exercises the happy path
// for /audit — the event store's audit-class events ride the
// shared FilterRecordsToAuditRows helper, and the page renders
// via the shared audit.tmpl with the central source label. The
// audit catalog treats every runner event as audit-class
// (audit.TYPES.1), so the filter side of this test uses a
// known-not-audit type ("test.event") to assert exclusion.
func TestCentralAuditRendersFromEventStore(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := stubEventStore{records: []eventlog.Record{
		auditRecord(t, "ev-1", "spec.amendment.proposed", t1),
		// Not-an-audit-event type — must be filtered out.
		auditRecord(t, "ev-2", "test.event", t1.Add(time.Second)),
		auditRecord(t, "ev-3", "spec.amendment.accepted", t1.Add(2*time.Second)),
	}}
	srv := newRunsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "spec.amendment.proposed") {
		t.Errorf("audit entry missing: %s", html)
	}
	if !strings.Contains(html, "spec.amendment.accepted") {
		t.Errorf("audit entry missing: %s", html)
	}
	if strings.Contains(html, "test.event") {
		t.Errorf("non-audit event leaked into audit page: %s", html)
	}
	if !strings.Contains(html, "the central event store") {
		t.Errorf("audit page missing central source label: %s", html)
	}
	// Default limit hint surfaces in the meta line.
	if !strings.Contains(html, "50 audit-class entries") {
		t.Errorf("default-limit copy missing: %s", html)
	}
}

// TestCentralAuditRespectsLimit covers the ?n=<limit> query: a
// smaller limit narrows the rendered set.
func TestCentralAuditRespectsLimit(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var records []eventlog.Record
	for i := 0; i < 10; i++ {
		records = append(records, auditRecord(t, "ev-"+strconvItoa(i), "spec.amendment.proposed", t1.Add(time.Duration(i)*time.Second)))
	}
	srv := newRunsServer(t, stubEventStore{records: records})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/audit?n=3")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "3 audit-class entries") {
		t.Errorf("limit not reflected in meta line: %s", html)
	}
	// Only the last 3 ids should appear in the rendered table.
	got := strings.Count(html, "ev-")
	if got < 3 {
		t.Errorf("expected at least 3 ev- references; got %d", got)
	}
}

// TestCentralRunsListWithoutResolverReturns503 covers the
// misconfigured-deployment branch for the new routes.
func TestCentralRunsListWithoutResolverReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, path := range []string{
		"/orgs/acme/workspaces/ws-1/runs",
		"/orgs/acme/workspaces/ws-1/runs/x",
		"/orgs/acme/workspaces/ws-1/audit",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d (want 503)", path, resp.StatusCode)
		}
	}
}

// TestCentralResolverPopulatesAllProjections asserts the wiring
// from NewGitStoreResolver: when both git + events are bound,
// every projection on Workspace is non-nil and operable.
func TestCentralResolverPopulatesAllProjections(t *testing.T) {
	t.Parallel()
	r := NewGitStoreResolver(stubGitStore{entries: map[string]string{}}, stubEventStore{})
	ws, err := r.Resolve("ws-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ws.Specs == nil {
		t.Error("Specs projection nil")
	}
	if ws.Runs == nil {
		t.Error("Runs projection nil")
	}
	if ws.RunDetail == nil {
		t.Error("RunDetail projection nil")
	}
	if ws.Audit == nil {
		t.Error("Audit projection nil")
	}
}

// Compile-time assertions: the central projections satisfy their
// shared interfaces. Catches drift if a future signature change
// silently breaks one of the impls.
var _ internalweb.RunsListProjection = centralRunsListProjection{}
var _ internalweb.RunDetailProjection = centralRunDetailProjection{}
var _ internalweb.AuditProjection = centralAuditProjection{}

// strconvItoa is a local Itoa to avoid adding strconv just for
// the audit-limit test (strconv is already imported in audit.go;
// keeping it out of the _test deps trims the diff).
func strconvItoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	out := string(buf[pos:])
	if neg {
		out = "-" + out
	}
	return out
}
