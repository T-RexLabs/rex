package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestAmendment(t *testing.T, root, stem, target, date, state, summary string) string {
	t.Helper()
	dir := filepath.Join(root, ".rex", "specs", "_proposed")
	if state == "accepted" {
		dir = filepath.Join(dir, "_accepted")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join([]string{
		"amendment_for: " + target,
		"amendment_date: " + date,
		"state: " + state,
		"summary: |",
		"  " + summary,
		"",
	}, "\n")
	path := filepath.Join(dir, stem+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", stem, err)
	}
	return path
}

func TestAmendmentsListRendersEmpty(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	srv := newTestServer(t, root)

	resp, err := http.Get(srv.URL + "/amendments")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"amendments", "no amendments"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestAmendmentsListRendersRows(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "extend amend")
	writeTestAmendment(t, root, "audit-amendment-2026-05-08", "audit", "2026-05-08", "accepted", "older")
	srv := newTestServer(t, root)

	resp, err := http.Get(srv.URL + "/amendments")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"cli-amendment-2026-05-10", "audit-amendment-2026-05-08", "extend amend", "proposed", "accepted"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestAmendmentsListFilterByFor(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "for cli")
	writeTestAmendment(t, root, "audit-amendment-2026-05-08", "audit", "2026-05-08", "proposed", "for audit")
	srv := newTestServer(t, root)

	resp, err := http.Get(srv.URL + "/amendments?for=cli")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "cli-amendment-2026-05-10") {
		t.Errorf("filter dropped expected row:\n%s", body)
	}
	if strings.Contains(string(body), "audit-amendment-2026-05-08") {
		t.Errorf("filter kept wrong row:\n%s", body)
	}
}

func TestAmendmentsDetailRenders(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "extend amend")
	srv := newTestServer(t, root)

	resp, err := http.Get(srv.URL + "/amendments/cli-amendment-2026-05-10")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		"cli-amendment-2026-05-10",
		"extend amend",
		"<form method=\"post\" action=\"/amendments/cli-amendment-2026-05-10/accept\"",
		"<form method=\"post\" action=\"/amendments/cli-amendment-2026-05-10/reject\"",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestAmendmentsDetailHidesActionsWhenAccepted(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "accepted", "already in")
	srv := newTestServer(t, root)

	resp, err := http.Get(srv.URL + "/amendments/cli-amendment-2026-05-10")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "/accept") {
		t.Errorf("accepted detail should not render accept button:\n%s", body)
	}
	if strings.Contains(string(body), "/reject") {
		t.Errorf("accepted detail should not render reject button:\n%s", body)
	}
}

func TestAmendmentsDetailNotFound(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	srv := newTestServer(t, root)

	resp, err := http.Get(srv.URL + "/amendments/ghost")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestAmendmentsAcceptMovesAndRedirects(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	src := writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "extend amend")
	srv := newTestServer(t, root)

	client := noRedirectClient()
	req, _ := http.NewRequest("POST", srv.URL+"/amendments/cli-amendment-2026-05-10/accept", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("want 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/amendments/cli-amendment-2026-05-10") {
		t.Errorf("unexpected redirect: %q", loc)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
	dst := filepath.Join(root, ".rex", "specs", "_proposed", "_accepted", "cli-amendment-2026-05-10.yaml")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !strings.Contains(string(body), "state: accepted") {
		t.Errorf("state not rewritten: %s", body)
	}

	// Verify the audit row landed in events.log.
	logBytes, _ := os.ReadFile(filepath.Join(root, ".rex", "events.log"))
	if !strings.Contains(string(logBytes), "spec.amendment.accepted") {
		t.Errorf("events.log missing accepted row:\n%s", logBytes)
	}
}

func TestAmendmentsRejectDeletesAndRedirects(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	src := writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "delete me")
	srv := newTestServer(t, root)

	client := noRedirectClient()
	req, _ := http.NewRequest("POST", srv.URL+"/amendments/cli-amendment-2026-05-10/reject", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("want 303, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Location"), "/amendments") {
		t.Errorf("unexpected redirect: %q", resp.Header.Get("Location"))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}

	logBytes, _ := os.ReadFile(filepath.Join(root, ".rex", "events.log"))
	if !strings.Contains(string(logBytes), "spec.amendment.rejected") {
		t.Errorf("events.log missing rejected row:\n%s", logBytes)
	}
}

func TestSpecDetailShowsAmendmentsPanel(t *testing.T) {
	t.Parallel()
	root := initWorkspace(t, "ws")
	// Spec the panel hosts.
	specBody := "spec_version: 1\nmetadata:\n  id: cli\n  name: CLI\n  state: draft\ntasks: []\n"
	if err := os.WriteFile(filepath.Join(root, ".rex", "specs", "cli.yaml"), []byte(specBody), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	writeTestAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "extend amend")

	srv := newTestServer(t, root)
	resp, err := http.Get(srv.URL + "/specs/cli")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		"proposed amendments",
		"cli-amendment-2026-05-10",
		"/amendments?for=cli",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}
