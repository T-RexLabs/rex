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

func TestRunCommandsInheritWorkspaceFlagFromParent(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "--workspace", dir, "start",
		"--shell", "echo parent-workspace",
		"--run-id", "parent-run-1",
	); err != nil {
		t.Fatalf("run start with parent workspace: %v", err)
	}

	out, err := executeCommand(t, "run", "--workspace", dir, "list")
	if err != nil {
		t.Fatalf("run list with parent workspace: %v\n%s", err, out)
	}
	if !strings.Contains(out, "parent-run-1") {
		t.Fatalf("expected parent-run-1 in output: %s", out)
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

func TestRunStartRequiresExactlyOneFlavor(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo hi",
		"--harness", "claude-code",
		"--prompt", "hi",
	)
	if err == nil {
		t.Fatal("expected exclusivity error")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunStartHarnessRequiresPrompt(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--harness", "claude-code",
	)
	if err == nil {
		t.Fatal("expected --prompt required error")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunStartHarnessRejectsUnknownAdapter(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--harness", "no-such-harness",
		"--prompt", "hi",
	)
	if err == nil {
		t.Fatal("expected unknown-adapter error")
	}
	if !strings.Contains(err.Error(), "no adapter registered") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestRunAttachReplaysCompletedRun(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo watch-me",
		"--run-id", "watch-1",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	out, err := executeCommand(t, "run", "attach", "--workspace", dir, "watch-1")
	if err != nil {
		t.Fatalf("run watch: %v\n%s", err, out)
	}
	for _, want := range []string{"run.started", "node.started", "node.succeeded", "run.completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestRunAttachExitsOnTerminalEvent(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "true",
		"--run-id", "watch-2",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// If the watch loop didn't exit on run.completed, this would
	// hang and the test framework would time out the whole suite.
	out, err := executeCommand(t, "run", "attach", "--workspace", dir, "watch-2")
	if err != nil {
		t.Fatalf("run watch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "run.completed") {
		t.Fatalf("expected run.completed in output\n%s", out)
	}
}

func TestRunAttachAcceptsRunIDPrefix(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo prefix-test",
		"--run-id", "prefix-test-12345",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	out, err := executeCommand(t, "run", "attach", "--workspace", dir, "prefix-")
	if err != nil {
		t.Fatalf("run watch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "run.completed") {
		t.Fatalf("expected run.completed via prefix lookup\n%s", out)
	}
}

func TestRunAttachUnknownRunErrors(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "attach", "--workspace", dir, "nope")
	if err == nil {
		t.Fatal("expected error for unknown run")
	}
}

func TestRunAttachJSONOutput(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo json-test",
		"--run-id", "json-1",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	out, err := executeCommand(t, "run", "attach", "--workspace", dir, "--json", "json-1")
	if err != nil {
		t.Fatalf("run watch --json: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected >=4 JSON lines (run.started + node.* + run.completed), got %d:\n%s", len(lines), out)
	}
	for i, ln := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", i, err, ln)
		}
		if rec["type"] == "" {
			t.Errorf("line %d missing type: %s", i, ln)
		}
	}
}

func TestRunWatchAliasStillWorks(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo alias",
		"--run-id", "alias-1",
		"--quiet",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	out, err := executeCommand(t, "run", "watch", "--workspace", dir, "alias-1")
	if err != nil {
		t.Fatalf("run watch (alias): %v\n%s", err, out)
	}
	if !strings.Contains(out, "run.completed") {
		t.Fatalf("alias did not stream events: %s", out)
	}
}

func TestRunStartQuietSuppressesLiveStream(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	out, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo quiet-test",
		"--run-id", "quiet-1",
		"--quiet",
	)
	if err != nil {
		t.Fatalf("run start --quiet: %v\n%s", err, out)
	}
	if strings.Contains(out, "run.started") || strings.Contains(out, "node.started") {
		t.Fatalf("--quiet should suppress event stream:\n%s", out)
	}
	// final summary still surfaces
	if !strings.Contains(out, "completed") {
		t.Fatalf("expected final summary in --quiet mode:\n%s", out)
	}
}

func TestRunStartAttachedStreamsLiveEvents(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	out, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo attached-test",
		"--run-id", "attached-1",
	)
	if err != nil {
		t.Fatalf("run start: %v\n%s", err, out)
	}
	for _, want := range []string{"run.started", "node.started", "node.succeeded", "run.completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("default-attached output missing %q\n%s", want, out)
		}
	}
}

func TestRunStartDetachIsDeferred(t *testing.T) {
	t.Parallel()

	dir := initWorkspaceForRunTest(t)
	_, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo nope",
		"--detach",
	)
	if err == nil {
		t.Fatal("expected --detach to be rejected as deferred")
	}
	if !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("error wording: %v", err)
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
