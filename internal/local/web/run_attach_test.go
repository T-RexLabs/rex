package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// TestRunNewRendersAttachFields confirms /runs/new ships the
// from_task + spec_refs inputs the form was missing before this
// commit. Without these fields users can't attach a run to a
// spec from the web UI.
func TestRunNewRendersAttachFields(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-render")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/runs/new")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		"attach to a spec",
		`name="from_task"`,
		`name="spec_refs"`,
		`placeholder="execution.dag-primitives"`,
		`placeholder="execution.PRIM.6, sync.ORDER.3"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body[:minInt(len(body), 3000)])
		}
	}
}

// TestRunNewPrefillFromQueryString covers the deep-link case:
// /runs/new?from_task=... preselects the field so a "start a
// run for this task" link from anywhere lands here ready to go.
func TestRunNewPrefillFromQueryString(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-prefill")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL +
		"/runs/new?from_task=execution.dag-primitives" +
		"&spec_ref=execution.PRIM.6&spec_ref=execution.PRIM.5")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		`value="execution.dag-primitives"`,
		`value="execution.PRIM.6, execution.PRIM.5"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body:\n%s", want, body)
		}
	}
}

// TestRunStartShellRecordsAttachment posts a shell run with the
// new fields and confirms the recovered run.started event
// carries from_task + spec_refs verbatim.
func TestRunStartShellRecordsAttachment(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-run-attach-shell")
	hs := newTestServer(t, root)

	form := strings.NewReader(
		"run_type=shell&shell=true" +
			"&from_task=execution.dag-primitives" +
			"&spec_refs=execution.PRIM.6, execution.PRIM.5",
	)
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

	// Find the run.started event we just appended and decode it.
	started := readRunStartedEvent(t, root)
	if started.FromTask != "execution.dag-primitives" {
		t.Fatalf("from_task: %q", started.FromTask)
	}
	want := []string{"execution.PRIM.6", "execution.PRIM.5"}
	if len(started.SpecRefs) != len(want) {
		t.Fatalf("spec_refs: %v", started.SpecRefs)
	}
	for i, ref := range want {
		if started.SpecRefs[i] != ref {
			t.Fatalf("spec_refs[%d]: got %q want %q", i, started.SpecRefs[i], ref)
		}
	}
}

// TestRunStartRejectsMalformedFromTask covers the simple
// validation: from_task without a dot separator is a 400 with
// the user's input echoed back so they can fix the typo.
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
	// The malformed value must echo back so the user can edit.
	if !strings.Contains(body, `value="just-a-task-id"`) {
		t.Fatalf("expected echoed value: %s", body)
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
