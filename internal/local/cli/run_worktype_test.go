package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// readRunStartedWorkType pulls the `work_type` field from the most
// recent run.started event in the workspace's events.log.
func readRunStartedWorkType(t *testing.T, root string) string {
	t.Helper()
	events := readAuditEvents(t, root, "run.started")
	if len(events) == 0 {
		t.Fatal("no run.started events recorded")
	}
	last := events[len(events)-1]
	var p struct {
		WorkType string `json:"work_type"`
	}
	if err := json.Unmarshal(last.Payload, &p); err != nil {
		t.Fatalf("decode run.started: %v", err)
	}
	return p.WorkType
}

func TestRunStartDefaultsWorkTypeToNonSpec(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "run", "start",
		"--workspace", root, "--shell", "true", "--run-id", "r-default-wt"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	if got := readRunStartedWorkType(t, root); got != "non_spec" {
		t.Fatalf("default work_type: got %q, want non_spec", got)
	}
}

func TestRunStartHonoursExplicitWorkType(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "run", "start",
		"--workspace", root,
		"--shell", "true",
		"--run-id", "r-explicit",
		"--work-type", "question",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}
	if got := readRunStartedWorkType(t, root); got != "question" {
		t.Fatalf("explicit work_type not honoured: got %q", got)
	}
}

func TestRunStartRejectsBadWorkType(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "run", "start",
		"--workspace", root,
		"--shell", "true",
		"--work-type", "nonsense",
	)
	if err == nil {
		t.Fatal("expected error for invalid --work-type")
	}
	if !strings.Contains(err.Error(), "invalid --work-type") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestRunStartFromTaskInfersSpec(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())

	// Author a single-task spec from scratch — we avoid `rex spec
	// create` here because the scaffold ships its own example
	// task and we'd have to overwrite anyway. A targeted body
	// keeps the test on the run.started provenance bits.
	specPath := strings.TrimSpace(root) + "/.rex/specs/demo.yaml"
	body := `spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
tasks:
  - id: greet
    description: say hello
    state: todo
    references: []
    run:
      kind: shell
      command: ["true"]
`
	if err := os.WriteFile(specPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	if _, err := executeCommand(t, "run", "start",
		"--workspace", root,
		"--from-task", "demo.greet",
		"--run-id", "r-fromtask",
	); err != nil {
		t.Fatalf("run start --from-task: %v", err)
	}

	if got := readRunStartedWorkType(t, root); got != "spec" {
		t.Fatalf("--from-task should imply spec; got %q", got)
	}
}

// TestRunStartedEventCarriesActor covers the bug fix that brought
// run-lifecycle events in line with workspace-/spec-/repo- audit
// events: the events.log writer now stamps Actor from the
// authenticated signer that runtask.Open auto-loads (or that the
// CLI threads through via WithSigner).
//
// Pre-fix, every run.* and node.* event landed with empty Actor
// because runtask.Open didn't pass Actor / Sign in the
// eventlog.WriterConfig. This test guards against the regression.
func TestRunStartedEventCarriesActor(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "run", "start",
		"--workspace", root, "--shell", "true", "--run-id", "r-actor-1"); err != nil {
		t.Fatalf("run start: %v", err)
	}
	events := readAuditEvents(t, root, "run.started")
	if len(events) == 0 {
		t.Fatal("no run.started events recorded")
	}
	last := events[len(events)-1]
	if last.Actor == "" {
		t.Fatalf("run.started must carry an Actor; got empty")
	}
	if !strings.HasPrefix(last.Actor, "l-") {
		t.Fatalf("Actor should be a local-scoped identity (prefix l-); got %q", last.Actor)
	}
	if last.Signature == "" {
		t.Fatalf("signed writer must produce a non-empty Signature; got empty")
	}
}

func TestScheduleTriggerStampsScheduledWorkType(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "schedule", "add", "--workspace", root,
		"once", "--cron", "0 0 * * *", "--shell", "true"); err != nil {
		t.Fatalf("schedule add: %v", err)
	}
	if _, err := executeCommand(t, "schedule", "trigger", "--workspace", root, "once"); err != nil {
		t.Fatalf("schedule trigger: %v", err)
	}
	if got := readRunStartedWorkType(t, root); got != "scheduled" {
		t.Fatalf("scheduled run should stamp work_type=scheduled; got %q", got)
	}
}
