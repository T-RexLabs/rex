package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// seedRunWithProvenance writes a single run.started event whose
// payload carries spec_refs + from_task — the Phase-C linkage
// surface — followed by a run.completed so the run shows up
// terminal in the listing.
func seedRunWithProvenance(t *testing.T, root, runID, fromTask string, specRefs []string) {
	t.Helper()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(root, ".rex", "events.log"),
		WorkspaceID: "test-ws",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	started := map[string]any{
		"run_id":     runID,
		"started_at": now,
	}
	if fromTask != "" {
		started["from_task"] = fromTask
	}
	if len(specRefs) > 0 {
		started["spec_refs"] = specRefs
	}
	body, err := json.Marshal(started)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := w.Append("run.started", 1, body); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := w.Append("run.completed", 1,
		json.RawMessage(`{"run_id":"`+runID+`","completed_at":"`+now+`"}`)); err != nil {
		t.Fatalf("append completed: %v", err)
	}
}

// TestSpecDetailRendersPerTaskRunHistory drives the Phase-C
// affordance end-to-end: a workspace with two runs in different
// tasks must render two distinct run-history blocks under their
// respective task ids on /specs/<id>?tab=tasks.
func TestSpecDetailRendersPerTaskRunHistory(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-perm-runs")
	// Drop a real spec into .rex/specs/ so the page resolves.
	specBody := `spec_version: 1
metadata:
  id: phase-c
  name: Phase C target
  state: draft
tasks:
  - id: alpha
    description: alpha task
    state: todo
  - id: beta
    description: beta task
    state: todo
`
	specsDir := filepath.Join(root, ".rex", "specs")
	if err := writeFileAt(specsDir, "phase-c.yaml", specBody); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	seedRunWithProvenance(t, root, "r-alpha-1", "phase-c.alpha", nil)
	seedRunWithProvenance(t, root, "r-alpha-2", "phase-c.alpha", nil)
	seedRunWithProvenance(t, root, "r-beta-only", "phase-c.beta", nil)

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/specs/phase-c?tab=tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body=%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		"r-alpha-1", "r-alpha-2", "r-beta-only",
		"task-runs",      // <details class="task-runs"> wrapper
		"task-runs-list", // the <ul>
		`href="/runs/`,   // each row links to the run detail page
		"2 runs",         // alpha bucket has two runs
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body)
		}
	}
}

// TestSpecDetailRendersUntaskedRuns covers the secondary block:
// a run that cites the spec via spec_refs but doesn't name a
// task surfaces in the "runs citing this spec without a task"
// list rather than under any task row.
func TestSpecDetailRendersUntaskedRuns(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-untasked")
	specBody := `spec_version: 1
metadata:
  id: phase-c
  name: Phase C
  state: draft
tasks:
  - id: only-task
    description: only
    state: todo
`
	if err := writeFileAt(filepath.Join(root, ".rex", "specs"), "phase-c.yaml", specBody); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	seedRunWithProvenance(t, root, "r-spec-only", "", []string{"phase-c.X.1"})

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/specs/phase-c?tab=tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "untasked-runs") {
		t.Fatalf("expected untasked-runs block: %s", body)
	}
	if !strings.Contains(body, "r-spec-only") {
		t.Fatalf("expected r-spec-only in body: %s", body)
	}
}

// TestRunDetailRendersSpecLink confirms the run-detail header
// renders the spec linkage block when from_task / spec_refs are
// recorded on the run.started event.
func TestRunDetailRendersSpecLink(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-link")
	seedRunWithProvenance(t, root, "r-linked", "execution.dag-primitives",
		[]string{"execution.PRIM.6"})

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/runs/r-linked")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		"launched from",
		`href="/specs/execution#dag-primitives"`,
		`href="/specs/execution"`, // spec_refs link
		"execution.PRIM.6",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body)
		}
	}
}

// writeFileAt is a tiny helper that mkdirs then writes a file.
// Local to this test file so other tests don't have to touch
// the helper for one-off seed specs.
func writeFileAt(dir, name, body string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}
