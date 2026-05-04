package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuditPageEmpty(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-audit-empty")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "no audit entries yet") {
		t.Fatalf("expected empty state: %s", body)
	}
}

func TestAuditPageRendersAuditClassEvents(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-audit")
	seedRunEvents(t, root, "audit-run")

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"run.started", "node.started", "node.succeeded", "run.completed"} {
		if !strings.Contains(body, want) {
			t.Errorf("/audit missing %q", want)
		}
	}
	// Header is rendered.
	if !strings.Contains(body, "audit log") {
		t.Fatalf("missing header: %s", body)
	}
}

func TestAuditPageHonorsLimitQuery(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-audit-limit")
	seedRunEvents(t, root, "limit-run")
	seedRunEvents(t, root, "another-run")

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/audit?n=2")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "last 2 audit-class entries") {
		t.Fatalf("limit not surfaced: %s", body)
	}
	// Two seeded runs × 4 events each = 8 audit-class events;
	// n=2 keeps only the most recent 2.
	rowCount := strings.Count(body, "<tr>") - 1 // subtract header row
	if rowCount != 2 {
		t.Fatalf("expected 2 data rows, got %d", rowCount)
	}
}

func TestAuditPageInvalidLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-audit-bad-limit")
	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/audit?n=garbage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "last 50 audit-class entries") {
		t.Fatalf("expected default 50: %s", body)
	}
}
