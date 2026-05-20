package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// emitRunStarted writes a RunStartedEvent directly to the
// workspace's events.log. We use this rather than going through
// `rex run start` because the latter has a mutually-exclusive
// flag group ([shell, harness, from-task]) that prevents seeding
// a shell run with from-task in one call.
func emitRunStarted(t *testing.T, root, runID, fromTask string, specRefs []string) {
	t.Helper()
	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        filepath.Join(root, ".rex", "events.log"),
		WorkspaceID: "spec-runs-test",
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()
	body, err := json.Marshal(runner.RunStartedEvent{
		RunID:     runID,
		StartedAt: time.Now().UTC(),
		SpecRefs:  specRefs,
		FromTask:  fromTask,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := w.Append(runner.EventTypeRunStarted, runner.EventVersion, body); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestSpecRunsListsRunsByFromTask seeds two runs (one launched
// from execution.dag-primitives, one ad-hoc) and asserts the
// `rex spec runs execution` command surfaces only the spec-cited
// run.
func TestSpecRunsListsRunsByFromTask(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	emitRunStarted(t, dir, "r-spec-cited", "execution.dag-primitives",
		[]string{"execution.PRIM.6"})
	emitRunStarted(t, dir, "r-adhoc", "", nil)

	out, err := executeCommand(t, "spec", "runs", "execution",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("spec runs: %v\n%s", err, out)
	}
	if !strings.Contains(out, "r-spec-cited") {
		t.Fatalf("expected cited run in output: %s", out)
	}
	if strings.Contains(out, "r-adhoc") {
		t.Fatalf("ad-hoc run should not appear: %s", out)
	}
}

func TestSpecRunsTaskFilter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	emitRunStarted(t, dir, "r-prim", "execution.dag-primitives", nil)
	emitRunStarted(t, dir, "r-life", "execution.run-lifecycle-cli", nil)

	out, err := executeCommand(t, "spec", "runs", "execution",
		"--task", "dag-primitives",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("spec runs: %v\n%s", err, out)
	}
	if !strings.Contains(out, "r-prim") {
		t.Fatalf("expected r-prim in output: %s", out)
	}
	if strings.Contains(out, "r-life") {
		t.Fatalf("r-life should be filtered out: %s", out)
	}
}

// TestSpecRunsJSONOutput exercises the --json shape so scripted
// callers don't need to scrape the table renderer.
func TestSpecRunsJSONOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	emitRunStarted(t, dir, "r-json", "audit.audit-storage", nil)

	out, err := executeCommand(t, "spec", "runs", "audit",
		"--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("spec runs --json: %v\n%s", err, out)
	}
	var rec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
	}
	if rec["from_task"] != "audit.audit-storage" {
		t.Fatalf("from_task: %v", rec)
	}
}

// TestSpecRunsMatchesSpecRefsPrefix covers the spec_refs
// (rather than from_task) match path: a run whose only spec
// linkage is a prefix-matching ACID still surfaces.
func TestSpecRunsMatchesSpecRefsPrefix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	emitRunStarted(t, dir, "r-prefix", "", []string{"sync.ORDER.3"})

	out, err := executeCommand(t, "spec", "runs", "sync",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("spec runs: %v\n%s", err, out)
	}
	if !strings.Contains(out, "r-prefix") {
		t.Fatalf("expected r-prefix surfaced via spec_refs: %s", out)
	}
}

// TestSpecRunsEmptyResult prints a friendly message when no runs
// cite the spec, instead of "no runs yet".
func TestSpecRunsEmptyResult(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "spec", "runs", "ghost",
		"--workspace", dir)
	if err != nil {
		t.Fatalf("spec runs: %v", err)
	}
	if !strings.Contains(out, "no runs cite spec ghost") {
		t.Fatalf("expected friendly empty msg: %s", out)
	}
}
