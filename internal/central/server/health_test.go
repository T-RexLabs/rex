package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

func parseFingerprintForTest(s string) (identity.Fingerprint, error) {
	return identity.ParseFingerprint(s)
}

func newHealthTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

func TestHealthAlwaysOK(t *testing.T) {
	t.Parallel()
	_, hs := newHealthTestServer(t)
	resp, err := http.Get(hs.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field: %q", body["status"])
	}
}

func TestReadyReturnsReadyForInMemoryStore(t *testing.T) {
	t.Parallel()
	_, hs := newHealthTestServer(t)
	resp, err := http.Get(hs.URL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ready" || body["db"] != "in-memory" {
		t.Fatalf("body: %+v", body)
	}
}

// flakyPinger satisfies Pinger but always errors. Used to drive
// /ready's 503 path without spinning a real Postgres.
type flakyPinger struct{ Store }

func (flakyPinger) Ping(_ context.Context) error { return errors.New("offline") }

func TestReadyReturns503WhenPingFails(t *testing.T) {
	t.Parallel()
	srv, err := New(Options{Store: flakyPinger{Store: NewMemoryStore()}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (expected 503)", resp.StatusCode)
	}
}

func TestMetricsExposesPrometheusFormat(t *testing.T) {
	t.Parallel()
	_, hs := newHealthTestServer(t)

	resp, err := http.Get(hs.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content-type: %q", got)
	}
	body, _ := readBody(resp)
	for _, want := range []string{
		"# HELP rex_central_events_appended_total",
		"# TYPE rex_central_events_appended_total counter",
		"rex_central_events_appended_total 0",
		"rex_central_events_duplicate_total",
		"rex_central_audit_events_total",
		"rex_central_push_requests_total",
		"rex_central_push_conflicts_total",
		"rex_central_auth_challenges_total",
		"rex_central_active_sessions",
		"rex_central_process_uptime_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

func TestMetricsRecordsAppendedEvent(t *testing.T) {
	t.Parallel()
	srv, _ := newHealthTestServer(t)
	ctx := context.Background()

	// Use an audit-class event type so AuditEvents also ticks.
	r := eventlog.Record{
		ID: "m1", Type: "workspace.created", Version: 1,
		Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws", Payload: []byte(`{}`),
	}
	added, err := srv.Store().Append(ctx, r)
	if err != nil || !added {
		t.Fatalf("Append: added=%v err=%v", added, err)
	}
	srv.recordEvent(r.Type, true)

	// Duplicate path
	srv.recordEvent(r.Type, false)

	snap := srv.Metrics().Snapshot()
	if snap.EventsAppended != 1 {
		t.Errorf("EventsAppended: %d", snap.EventsAppended)
	}
	if snap.EventsDuplicate != 1 {
		t.Errorf("EventsDuplicate: %d", snap.EventsDuplicate)
	}
	if snap.AuditEvents != 1 {
		t.Errorf("AuditEvents (expected workspace.created to be audit): %d", snap.AuditEvents)
	}
}

func TestMetricsActiveSessionsReflectsAuth(t *testing.T) {
	t.Parallel()
	srv, _ := newHealthTestServer(t)

	// 0 sessions on a fresh server.
	snap := srv.Metrics().Snapshot()
	if snap.ActiveSessions != 0 {
		t.Fatalf("fresh server active sessions: %d", snap.ActiveSessions)
	}
	// Issue tokens directly via authState (avoids the full
	// challenge/verify dance the integration tests cover
	// elsewhere). 16-char hex fingerprints per identity-
	// and-trust.KEY.4.
	for _, fp := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		parsed, err := parseFingerprintForTest(fp)
		if err != nil {
			t.Fatalf("parse fp: %v", err)
		}
		if _, err := srv.auth.issueToken(parsed, "sync"); err != nil {
			t.Fatalf("issueToken: %v", err)
		}
	}
	snap = srv.Metrics().Snapshot()
	if snap.ActiveSessions != 2 {
		t.Errorf("active sessions after 2 issues: %d", snap.ActiveSessions)
	}
}

// readBody is a tiny helper to read an http.Response body as a
// string. Defined here so health_test.go is self-contained.
func readBody(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String(), nil
}
