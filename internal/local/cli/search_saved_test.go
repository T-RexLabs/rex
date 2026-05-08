package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchSavedAddListRunRemove(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())

	// add
	out, err := executeCommand(t, "search", "saved", "add", "--workspace", root,
		"recent-runs", "type:run.completed")
	if err != nil {
		t.Fatalf("saved add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "saved \"recent-runs\"") {
		t.Fatalf("output: %q", out)
	}
	path := filepath.Join(root, ".rex", "saved-searches.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "[searches.recent-runs]") || !strings.Contains(string(body), "type:run.completed") {
		t.Fatalf("on-disk shape: %s", body)
	}

	// list (text)
	out, err = executeCommand(t, "search", "saved", "list", "--workspace", root)
	if err != nil {
		t.Fatalf("saved list: %v", err)
	}
	if !strings.Contains(out, "recent-runs") || !strings.Contains(out, "workspace") {
		t.Fatalf("list output: %q", out)
	}

	// list --json
	out, err = executeCommand(t, "search", "saved", "list", "--workspace", root, "--json")
	if err != nil {
		t.Fatalf("saved list --json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["Name"] != "recent-runs" || rows[0]["Source"] != "workspace" {
		t.Fatalf("json shape: %v", rows)
	}

	// run — depends on the search index existing; the workspace
	// has no events that match this query, so we expect "no
	// matches" without an error.
	out, err = executeCommand(t, "search", "saved", "run", "--workspace", root, "recent-runs")
	if err != nil {
		t.Fatalf("saved run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("run output: %q", out)
	}

	// remove
	out, err = executeCommand(t, "search", "saved", "remove", "--workspace", root, "recent-runs")
	if err != nil {
		t.Fatalf("saved remove: %v", err)
	}
	if !strings.Contains(out, "removed \"recent-runs\"") {
		t.Fatalf("remove output: %q", out)
	}
	out, _ = executeCommand(t, "search", "saved", "list", "--workspace", root)
	if !strings.Contains(out, "no saved searches") {
		t.Fatalf("post-remove list: %q", out)
	}
}

func TestSearchSavedAddRejectsDuplicate(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "search", "saved", "add", "--workspace", root, "x", "q"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := executeCommand(t, "search", "saved", "add", "--workspace", root, "x", "q2")
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestSearchSavedAddRejectsBadName(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "search", "saved", "add", "--workspace", root, "Bad_Name", "q")
	if err == nil {
		t.Fatal("expected kebab-case rejection")
	}
	if !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestSearchSavedRunUnknownErrors(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "search", "saved", "run", "--workspace", root, "ghost")
	if err == nil {
		t.Fatal("expected unknown-saved error")
	}
	if !strings.Contains(err.Error(), "no saved search named") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestSearchSavedRemoveUnknownErrors(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "search", "saved", "remove", "--workspace", root, "ghost")
	if err == nil {
		t.Fatal("expected unknown-saved error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestSearchSavedAddMultiWordQueryConcatenates verifies that
// `rex search saved add <name> hello world` joins the trailing
// args into one query string, mirroring `rex search hello world`.
func TestSearchSavedAddMultiWordQueryConcatenates(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "search", "saved", "add", "--workspace", root,
		"two-word", "hello", "world"); err != nil {
		t.Fatalf("multi-arg add: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".rex", "saved-searches.toml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), `query = "hello world"`) {
		t.Fatalf("trailing args not joined: %s", body)
	}
}
