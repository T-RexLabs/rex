package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func appendToFile(t *testing.T, path, body string) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(body)
	return err
}

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

	// Create a spec with a single shell-recipe task.
	if _, err := executeCommand(t, "spec", "create", "--workspace", root, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}
	// Append a task with a recipe to the new spec.
	specPath := strings.TrimSpace(root) + "/.rex/specs/demo.yaml"
	body := `tasks:
  - id: greet
    description: say hello
    state: todo
    references: []
    run:
      kind: shell
      command: ["true"]
`
	// Use os.WriteFile via append rather than a separate test
	// helper — we just need the file to validate.
	if err := appendToFile(t, specPath, body); err != nil {
		t.Fatalf("append: %v", err)
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
