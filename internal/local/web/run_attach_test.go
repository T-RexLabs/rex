package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// seedSpecForFormPicker drops a tiny spec into the workspace's
// .rex/specs/ tree so /runs/new's from_task dropdown has
// something to render. Two tasks so the test can assert
// alphabetical ordering.
func seedSpecForFormPicker(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `spec_version: 1
metadata:
  id: phase-c
  name: Phase C target
  state: draft
tasks:
  - id: alpha
    description: alpha task description
    state: todo
  - id: beta
    description: beta task description
    state: todo
`
	if err := os.WriteFile(filepath.Join(dir, "phase-c.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

// TestRunNewRendersFromTaskDropdown confirms the harness panel
// has a real dropdown populated with every (spec, task) pair in
// the workspace, rather than the previous free-text input.
func TestRunNewRendersFromTaskDropdown(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-dropdown")
	seedSpecForFormPicker(t, root)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/runs/new")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		`<select`,
		`name="from_task"`,
		`<option value="">— none —</option>`,
		`value="phase-c.alpha"`,
		`value="phase-c.beta"`,
		"phase-c · alpha",
		"alpha task description",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body[:minInt(len(body), 4000)])
		}
	}
	// The CLI-only spec_refs text input must NOT render — it
	// was dropped from the form when the dropdown landed.
	if strings.Contains(body, `name="spec_refs"`) {
		t.Errorf("spec_refs text input should not render in the dropdown UI")
	}
	// The dropdown must live inside the harness panel so it's
	// disabled while shell is the active run type.
	idxPanel := strings.Index(body, `data-run-panel="harness"`)
	idxFromTask := strings.Index(body, `name="from_task"`)
	if idxPanel < 0 || idxFromTask < 0 || idxFromTask < idxPanel {
		t.Errorf("from_task picker should be inside the harness panel; idxPanel=%d idxFromTask=%d", idxPanel, idxFromTask)
	}
}

// TestRunNewPrefillFromQueryStringSelectsOption confirms the
// deep-link prefill still works with the dropdown surface — the
// matching <option> renders with `selected`.
func TestRunNewPrefillFromQueryStringSelectsOption(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-prefill-dropdown")
	seedSpecForFormPicker(t, root)
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/runs/new?from_task=phase-c.beta")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, `value="phase-c.beta" data-state="todo" selected`) {
		t.Errorf("expected phase-c.beta to be the selected option:\n%s", body[:minInt(len(body), 3000)])
	}
	// Prefill should auto-pick the harness panel so the picker
	// is enabled rather than disabled.
	if !strings.Contains(body, `value="harness" selected`) {
		t.Errorf("from_task prefill should default the run type to harness")
	}
}

// TestRunStartHarnessRecordsFromTask posts a shell run carrying
// a from_task value (the dropdown picks one) and asserts the
// recovered run.started event records it. We use a shell run
// even though the dropdown lives in the harness panel —
// validateFromTaskField doesn't gate on run type, so a shell
// POST with from_task is fine and the test stays lightweight
// (no harness adapter needed).
func TestRunStartShellRecordsFromTask(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-record")
	seedSpecForFormPicker(t, root)
	hs := newTestServer(t, root)

	form := strings.NewReader("run_type=shell&shell=true&from_task=phase-c.alpha")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		body := readBody(t, resp)
		t.Fatalf("expected 303, got %d body=%s", resp.StatusCode, body)
	}

	started := readRunStartedEvent(t, root)
	if started.FromTask != "phase-c.alpha" {
		t.Fatalf("from_task: %q", started.FromTask)
	}
	if len(started.SpecRefs) != 0 {
		t.Fatalf("spec_refs should be empty when only from_task is set: %v", started.SpecRefs)
	}
}

// TestRunStartRejectsMalformedFromTask covers the defensive
// validation: a craftable POST with a malformed from_task
// (the dropdown wouldn't produce one) is a 400 with the user's
// value echoed back.
func TestRunStartRejectsMalformedFromTask(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-bad")
	hs := newTestServer(t, root)

	form := strings.NewReader("run_type=shell&shell=true&from_task=just-a-task-id")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "must be in &lt;spec-id&gt;.&lt;task-id&gt; form") {
		t.Fatalf("expected hint in body: %s", body)
	}
}

// readRunStartedEvent walks the workspace events.log and decodes
// the first run.started event it finds.
func readRunStartedEvent(t *testing.T, root string) runner.RunStartedEvent {
	t.Helper()
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer r.Close()
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if rec.Type != runner.EventTypeRunStarted {
			continue
		}
		var ev runner.RunStartedEvent
		if err := json.Unmarshal(rec.Payload, &ev); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return ev
	}
	t.Fatalf("no run.started event in %s", root)
	return runner.RunStartedEvent{}
}
