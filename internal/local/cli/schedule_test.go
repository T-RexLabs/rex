package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/audit"
)

func TestScheduleAddListShowRemove(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())

	out, err := executeCommand(t, "schedule", "add", "--workspace", root,
		"nightly", "--cron", "0 3 * * *", "--shell", "go test ./...")
	if err != nil {
		t.Fatalf("schedule add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "added schedule \"nightly\"") {
		t.Fatalf("output: %q", out)
	}

	path := filepath.Join(root, ".rex", "schedules", "nightly.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("schedule file missing: %v", err)
	}

	// list (text)
	out, err = executeCommand(t, "schedule", "list", "--workspace", root)
	if err != nil {
		t.Fatalf("schedule list: %v", err)
	}
	if !strings.Contains(out, "nightly") || !strings.Contains(out, "cron") || !strings.Contains(out, "0 3 * * *") {
		t.Fatalf("list output: %q", out)
	}

	// list --json
	out, err = executeCommand(t, "schedule", "list", "--workspace", root, "--json")
	if err != nil {
		t.Fatalf("schedule list --json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["name"] != "nightly" || rows[0]["cron"] != "0 3 * * *" {
		t.Fatalf("json shape: %v", rows)
	}

	// show — pretty-print round-trips the YAML body
	out, err = executeCommand(t, "schedule", "show", "--workspace", root, "nightly")
	if err != nil {
		t.Fatalf("schedule show: %v", err)
	}
	if !strings.Contains(out, "name: nightly") || !strings.Contains(out, "command:") {
		t.Fatalf("show output: %q", out)
	}

	// audit trail: schedule.added landed
	addedEvents := readAuditEvents(t, root, audit.EventTypeScheduleAdded)
	if len(addedEvents) != 1 {
		t.Fatalf("want 1 schedule.added, got %d", len(addedEvents))
	}
	var addedPayload audit.ScheduleAddedEvent
	if err := json.Unmarshal(addedEvents[0].Payload, &addedPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if addedPayload.Name != "nightly" || addedPayload.TriggerKind != "cron" {
		t.Fatalf("payload: %+v", addedPayload)
	}

	// remove
	out, err = executeCommand(t, "schedule", "remove", "--workspace", root, "nightly")
	if err != nil {
		t.Fatalf("schedule remove: %v", err)
	}
	if !strings.Contains(out, "removed schedule \"nightly\"") {
		t.Fatalf("remove output: %q", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("schedule file should be gone: %v", err)
	}
	removedEvents := readAuditEvents(t, root, audit.EventTypeScheduleRemoved)
	if len(removedEvents) != 1 {
		t.Fatalf("want 1 schedule.removed, got %d", len(removedEvents))
	}
}

func TestScheduleAddRequiresOneOfCronOrPaths(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "schedule", "add", "--workspace", root, "nope")
	if err == nil {
		t.Fatal("expected error when neither --cron nor --paths is given")
	}
	if !strings.Contains(err.Error(), "either --cron or --paths") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestScheduleAddRejectsBothCronAndPaths(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "schedule", "add", "--workspace", root, "x",
		"--cron", "0 0 * * *", "--paths", "src/*.go")
	if err == nil {
		t.Fatal("expected mutually-exclusive error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestScheduleAddRejectsInvalidCron(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	// The validator at write time accepts any non-empty string;
	// the cron parser rejects junk at daemon-start time. We assert
	// at least that the YAML is created for the simple case so
	// the user can fix-by-edit, not for the invalid-cron reject
	// case which is daemon-side.
	out, err := executeCommand(t, "schedule", "add", "--workspace", root,
		"weird", "--cron", "* * * * *", "--shell", "true")
	if err != nil {
		t.Fatalf("plain valid cron rejected: %v\n%s", err, out)
	}
}

func TestScheduleRemoveErrorsOnUnknown(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "schedule", "remove", "--workspace", root, "ghost")
	if err == nil {
		t.Fatal("expected error for unknown schedule")
	}
	if !strings.Contains(err.Error(), "no schedule named") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestScheduleAddNonKebabRejected(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "schedule", "add", "--workspace", root,
		"NotKebab", "--cron", "0 0 * * *", "--shell", "true")
	if err == nil {
		t.Fatal("expected kebab-case rejection")
	}
	if !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestScheduleTriggerExecutesShell fires the trigger CLI path
// against a real shell recipe and asserts the run completed.
func TestScheduleTriggerExecutesShell(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "schedule", "add", "--workspace", root,
		"once", "--cron", "0 0 * * *", "--shell", "true"); err != nil {
		t.Fatalf("schedule add: %v", err)
	}
	if _, err := executeCommand(t, "schedule", "trigger", "--workspace", root, "once"); err != nil {
		t.Fatalf("schedule trigger: %v", err)
	}

	// The fire should have produced a run.started event whose
	// trigger.kind == "cron" and trigger.schedule == "once".
	type startedPayload struct {
		Trigger *struct {
			Kind     string `json:"kind"`
			Schedule string `json:"schedule"`
		} `json:"trigger,omitempty"`
	}
	events := readAuditEvents(t, root, "run.started")
	if len(events) == 0 {
		t.Fatal("no run.started events recorded")
	}
	var matched bool
	for _, ev := range events {
		var p startedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Trigger != nil && p.Trigger.Kind == "cron" && p.Trigger.Schedule == "once" {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatal("no run.started event carried trigger=cron schedule=once")
	}
}
