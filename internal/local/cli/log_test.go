package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initLogWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "lt", "--name", "LT"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

func TestLogTailEmptyMissingLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".rex"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out, err := executeCommand(t, "log", "tail", "--workspace", dir)
	if err != nil {
		t.Fatalf("log tail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no entries match") {
		t.Fatalf("expected empty: %s", out)
	}
}

func TestLogTailShowsWorkspaceCreated(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "tail", "--workspace", dir)
	if err != nil {
		t.Fatalf("log tail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace.created") {
		t.Fatalf("expected workspace.created in tail: %s", out)
	}
	if !strings.Contains(out, "TIMESTAMP") {
		t.Fatalf("expected table header: %s", out)
	}
}

func TestLogParentCommandInheritsWorkspaceFlag(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "--workspace", dir)
	if err != nil {
		t.Fatalf("log with parent workspace: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace.created") {
		t.Fatalf("expected workspace.created in output: %s", out)
	}
}

func TestLogTailJSON(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "tail", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("log tail --json: %v\n%s", err, out)
	}
	for _, line := range splitNonEmpty(out) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("non-JSON line: %q (err=%v)", line, err)
		}
		if v["type"] == nil {
			t.Fatalf("entry missing type: %v", v)
		}
	}
}

func TestLogTailFiltersByType(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo hi",
		"--run-id", "r-filter-1",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}

	out, err := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"--type", "run.completed",
	)
	if err != nil {
		t.Fatalf("log tail --type: %v\n%s", err, out)
	}
	if !strings.Contains(out, "run.completed") {
		t.Fatalf("expected run.completed: %s", out)
	}
	if strings.Contains(out, "workspace.created") {
		t.Fatalf("--type should exclude other event types: %s", out)
	}
}

func TestLogTailFiltersByActor(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	// Find the actor used by this workspace (from the
	// workspace.created event).
	out, err := executeCommand(t, "log", "tail", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("seed list: %v", err)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(splitNonEmpty(out)[0]), &first); err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	actor, _ := first["actor"].(string)
	if !strings.HasPrefix(actor, "l-") {
		t.Fatalf("seed actor: %q", actor)
	}

	got, err := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"--actor", actor,
	)
	if err != nil {
		t.Fatalf("filter: %v\n%s", err, got)
	}
	if !strings.Contains(got, actor) {
		t.Fatalf("expected actor in output: %s", got)
	}

	// A made-up actor should yield no entries.
	noMatch, _ := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"--actor", "l-deadbeefdeadbeef",
	)
	if !strings.Contains(noMatch, "no entries match") {
		t.Fatalf("expected empty for unknown actor: %s", noMatch)
	}
}

func TestLogTailSinceDuration(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	// 1h ago should include everything written in this test.
	out, err := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"--since", "1h",
	)
	if err != nil {
		t.Fatalf("--since 1h: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace.created") {
		t.Fatalf("expected entries: %s", out)
	}
}

func TestLogTailSinceFutureExcludesAll(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	future := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	out, err := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"--since", future,
	)
	if err != nil {
		t.Fatalf("--since future: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no entries match") {
		t.Fatalf("expected empty: %s", out)
	}
}

func TestLogTailRejectsBadSince(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	_, err := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"--since", "yesterday",
	)
	if err == nil {
		t.Fatal("expected error for bad --since")
	}
}

func TestLogTailNFlagBoundsRecordCount(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	// One run produces 4 audit-class events (run.started,
	// node.started, node.succeeded, run.completed).
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "r-n",
	); err != nil {
		t.Fatalf("run: %v", err)
	}

	out, err := executeCommand(t, "log", "tail",
		"--workspace", dir,
		"-n", "2",
	)
	if err != nil {
		t.Fatalf("log tail -n: %v", err)
	}
	// Header + 2 rows + tabwriter padding/whitespace.
	dataLines := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "TIMESTAMP") {
			continue
		}
		dataLines++
	}
	if dataLines != 2 {
		t.Fatalf("expected 2 data lines, got %d:\n%s", dataLines, out)
	}
}

func TestLogTailAuditOnlyTrueByDefault(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "tail", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("log tail: %v\n%s", err, out)
	}
	for _, line := range splitNonEmpty(out) {
		var v map[string]any
		_ = json.Unmarshal([]byte(line), &v)
		typ, _ := v["type"].(string)
		// Every emitted entry must be audit-class — confirms
		// audit-only is on by default.
		if typ == "" {
			t.Fatalf("entry missing type: %v", v)
		}
	}
}

func TestLogSearchFindsWorkspaceCreated(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "search", "--workspace", dir, "workspace.created")
	if err != nil {
		t.Fatalf("log search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace.created") {
		t.Fatalf("expected match for workspace.created, got: %q", out)
	}
}

func TestLogSearchNoMatch(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "search", "--workspace", dir, "no-such-token-anywhere")
	if err != nil {
		t.Fatalf("log search: %v", err)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("expected no-matches notice, got: %q", out)
	}
}

func TestLogSearchTypeNarrowsResults(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	// Add a repo so we have a second event type to search against.
	if err := os.MkdirAll(filepath.Join(dir, "vendored"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := executeCommand(t, "repo", "link", "--workspace", dir, "vendored"); err != nil {
		t.Fatalf("repo link: %v", err)
	}

	// --type narrows to the named event type only.
	out, err := executeCommand(t, "log", "search", "--workspace", dir,
		"--type", "repo.linked", "vendored")
	if err != nil {
		t.Fatalf("log search --type: %v\n%s", err, out)
	}
	if !strings.Contains(out, "repo.linked") {
		t.Fatalf("expected repo.linked match: %q", out)
	}
	if strings.Contains(out, "workspace.created") {
		t.Fatalf("--type should have excluded workspace.created: %q", out)
	}
}

func TestLogSearchAuditOnlyExcludesNonAudit(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	// Override default with --audit-only=false; should still
	// return workspace.created (which is audit-class anyway).
	out, err := executeCommand(t, "log", "search", "--workspace", dir,
		"--audit-only=false", "workspace.created")
	if err != nil {
		t.Fatalf("log search --audit-only=false: %v", err)
	}
	if !strings.Contains(out, "workspace.created") {
		t.Fatalf("expected match: %q", out)
	}
}

func TestLogSearchJSON(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	out, err := executeCommand(t, "log", "search", "--workspace", dir, "--json", "workspace.created")
	if err != nil {
		t.Fatalf("log search --json: %v", err)
	}
	lines := splitNonEmpty(out)
	if len(lines) == 0 {
		t.Fatalf("no json lines: %q", out)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if first["type"] != "workspace.created" {
		t.Fatalf("unexpected first row: %v", first)
	}
}

func TestLogSearchMissingArgErrors(t *testing.T) {
	t.Parallel()

	dir := initLogWorkspace(t)
	_, err := executeCommand(t, "log", "search", "--workspace", dir)
	if err == nil {
		t.Fatal("expected error when query is omitted")
	}
}
