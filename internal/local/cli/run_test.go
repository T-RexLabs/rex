package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initWorkspaceForRunTest is the common setup: tempdir + workspace
// init. Returns the workspace root path.
func initWorkspaceForRunTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "runs", "--name", "Runs"); err != nil {
		t.Fatalf("init: %v", err)
	}
	return dir
}

func TestRunStartShellSuccess(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	out, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo hello-rex",
		"--run-id", "test-run-1",
	)
	if err != nil {
		t.Fatalf("run start: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-rex") {
		t.Fatalf("stdout not surfaced: %s", out)
	}
	if !strings.Contains(out, "test-run-1") {
		t.Fatalf("run id not surfaced: %s", out)
	}
	if !strings.Contains(out, "completed") {
		t.Fatalf("status not surfaced: %s", out)
	}
	// events.log is non-empty.
	info, err := os.Stat(filepath.Join(dir, ".rex", "events.log"))
	if err != nil {
		t.Fatalf("events.log: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("events.log was not written to")
	}
}

func TestRunStartShellFailureReturnsError(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "sh -c \"exit 5\"",
		"--run-id", "fail-1",
	)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunStartRequiresShell(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start", "--workspace", dir)
	if err == nil {
		t.Fatal("expected --shell required error")
	}
}

func TestRunStartUnbalancedQuoteRejected(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", `echo "open`,
	)
	if err == nil {
		t.Fatal("unbalanced quote should error")
	}
	if !strings.Contains(err.Error(), "unbalanced") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunListEmptyWorkspace(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	out, err := executeCommand(t, "run", "list", "--workspace", dir)
	if err != nil {
		t.Fatalf("run list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no runs yet") {
		t.Fatalf("expected 'no runs yet': %s", out)
	}
}

func TestRunListAfterStart(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo first",
		"--run-id", "r1",
	); err != nil {
		t.Fatalf("run start r1: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo second",
		"--run-id", "r2",
	); err != nil {
		t.Fatalf("run start r2: %v", err)
	}

	out, err := executeCommand(t, "run", "list", "--workspace", dir)
	if err != nil {
		t.Fatalf("run list: %v\n%s", err, out)
	}
	for _, want := range []string{"r1", "r2", "completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("run list missing %q\n%s", want, out)
		}
	}
}

func TestRunListStatusFilter(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "ok",
	); err != nil {
		t.Fatalf("run start ok: %v", err)
	}
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "sh -c \"exit 1\"", "--run-id", "bad",
	); err == nil {
		t.Fatal("expected aborted run for exit-1 command")
	}

	out, err := executeCommand(t, "run", "list",
		"--workspace", dir,
		"--status", "completed",
	)
	if err != nil {
		t.Fatalf("run list: %v\n%s", err, out)
	}
	if strings.Contains(out, "bad") {
		t.Fatalf("status filter leaked aborted: %s", out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("status filter dropped completed: %s", out)
	}
}

func TestRunListJSON(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "j1",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}
	out, err := executeCommand(t, "run", "list", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	var rows []map[string]any
	for _, line := range splitNonEmpty(out) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("non-JSON line: %q", line)
		}
		rows = append(rows, v)
	}
	if len(rows) != 1 || rows[0]["run_id"] != "j1" {
		t.Fatalf("unexpected rows: %v", rows)
	}
}

func TestRunShowReplaysEvents(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "echo show-me", "--run-id", "show-1",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}
	out, err := executeCommand(t, "run", "show", "show-1", "--workspace", dir)
	if err != nil {
		t.Fatalf("run show: %v\n%s", err, out)
	}
	for _, want := range []string{"run.started", "node.started", "node.succeeded", "run.completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q\n%s", want, out)
		}
	}
}

func TestRunShowMissingRun(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "show", "nope", "--workspace", dir)
	if err == nil {
		t.Fatal("expected error for unknown run id")
	}
}

func TestRunShowJSONIsValidNDJSON(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "json-1",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}
	out, err := executeCommand(t, "run", "show", "json-1", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("run show --json: %v\n%s", err, out)
	}
	for _, line := range splitNonEmpty(out) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("non-JSON line: %q", line)
		}
		if v["type"] == nil {
			t.Fatalf("missing type field: %v", v)
		}
	}
}

func TestSplitShellCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want []string
	}{
		{"echo hi", []string{"echo", "hi"}},
		{`echo "hello world"`, []string{"echo", "hello world"}},
		{`echo  "a b"  c`, []string{"echo", "a b", "c"}},
		{"", nil},
	}
	for _, tc := range cases {
		got, err := splitShellCommand(tc.in)
		if err != nil {
			t.Fatalf("splitShellCommand(%q): %v", tc.in, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("splitShellCommand(%q): got %v want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("splitShellCommand(%q)[%d]: got %q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
