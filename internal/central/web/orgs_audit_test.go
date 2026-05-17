package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalweb "github.com/asabla/rex/internal/web"
)

// stubOrgAudit injects deterministic rows + a capture of the
// (orgID, limit) the handler called with so tests can assert
// both the rendered page and the projection wiring.
type stubOrgAudit struct {
	rows  []internalweb.AuditRow
	err   error
	calls []orgAuditCall
}

type orgAuditCall struct {
	OrgID string
	Limit int
}

func (s *stubOrgAudit) TailOrgAudit(orgID string, limit int) ([]internalweb.AuditRow, error) {
	s.calls = append(s.calls, orgAuditCall{OrgID: orgID, Limit: limit})
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

func newOrgAuditServer(t *testing.T, oa internalweb.OrgAuditProjection) *httptest.Server {
	t.Helper()
	s, err := New(Options{Version: "test", OrgAudit: oa})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestOrgAuditRendersRows covers the happy path: stub returns
// two rows and the rendered table contains both event types +
// the org id in the header.
func TestOrgAuditRendersRows(t *testing.T) {
	t.Parallel()
	oa := &stubOrgAudit{rows: []internalweb.AuditRow{
		{ID: "ev-1", Type: "org.member.invited", Actor: "c-fp-alice", Timestamp: "2026-05-17T10:00:00Z"},
		{ID: "ev-2", Type: "org.member.joined", Actor: "c-fp-alice", Timestamp: "2026-05-17T10:05:00Z"},
	}}
	srv := newOrgAuditServer(t, oa)
	resp, err := http.Get(srv.URL + "/orgs/acme/audit")
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
	if !strings.Contains(html, "org.member.invited") || !strings.Contains(html, "org.member.joined") {
		t.Errorf("rendered table missing event types: %s", html)
	}
	if !strings.Contains(html, "<code>acme</code>") {
		t.Errorf("org id missing from header: %s", html)
	}
	if len(oa.calls) != 1 || oa.calls[0].OrgID != "acme" {
		t.Errorf("projection calls: %+v", oa.calls)
	}
	if oa.calls[0].Limit != internalweb.AuditDefaultLimit {
		t.Errorf("default limit not used: %d", oa.calls[0].Limit)
	}
}

// TestOrgAuditHonoursLimitQuery covers the ?n= override + the
// upper/lower-bound guards on the limit value.
func TestOrgAuditHonoursLimitQuery(t *testing.T) {
	t.Parallel()
	oa := &stubOrgAudit{}
	srv := newOrgAuditServer(t, oa)
	resp, err := http.Get(srv.URL + "/orgs/acme/audit?n=25")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if len(oa.calls) != 1 || oa.calls[0].Limit != 25 {
		t.Errorf("limit not threaded: %+v", oa.calls)
	}
}

// TestOrgAuditEmptyShowsNotice covers the no-rows branch.
func TestOrgAuditEmptyShowsNotice(t *testing.T) {
	t.Parallel()
	srv := newOrgAuditServer(t, &stubOrgAudit{})
	resp, err := http.Get(srv.URL + "/orgs/acme/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no audit entries for this org yet") {
		t.Errorf("empty-state notice missing: %s", body)
	}
}

// TestOrgAuditUnconfiguredReturns503 covers the dev-mode wireup
// where --db is off and the projection is nil.
func TestOrgAuditUnconfiguredReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/orgs/acme/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d want 503", resp.StatusCode)
	}
}

// TestOrgAuditPropagatesProjectionError covers the storage-failure
// path (operator should see the error to debug; users won't get
// here behind any RBAC gate since failure is structural).
func TestOrgAuditPropagatesProjectionError(t *testing.T) {
	t.Parallel()
	srv := newOrgAuditServer(t, &stubOrgAudit{err: io.EOF})
	resp, err := http.Get(srv.URL + "/orgs/acme/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: %d want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "EOF") {
		t.Errorf("error not surfaced: %s", body)
	}
}
