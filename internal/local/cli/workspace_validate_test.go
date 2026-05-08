package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspaceYAML(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, ".rex", "workspace.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workspace.yaml: %v", err)
	}
}

func TestWorkspaceValidateOnFreshInit(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	out, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("expected ok on fresh init: %q", out)
	}
}

func TestWorkspaceValidateMissingRequiredKey(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `id: demo
name: Demo
state: active
`)
	out, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err == nil {
		t.Fatalf("expected error, got %q", out)
	}
	if !strings.Contains(out, "created_at") {
		t.Fatalf("expected error to mention created_at: %q", out)
	}
}

func TestWorkspaceValidateBadID(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `id: NotKebab
name: Demo
state: active
created_at: "2026-05-08T00:00:00Z"
`)
	_, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err == nil {
		t.Fatal("expected kebab-case error")
	}
	if !strings.Contains(err.Error(), "1 error") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestWorkspaceValidateBadState(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `id: demo
name: Demo
state: surprise
created_at: "2026-05-08T00:00:00Z"
`)
	out, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err == nil {
		t.Fatal("expected state error")
	}
	if !strings.Contains(out, "active/archived/deleted") {
		t.Fatalf("expected hint: %q", out)
	}
}

func TestWorkspaceValidateBadMultiRepoMode(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `id: demo
name: Demo
state: active
created_at: "2026-05-08T00:00:00Z"
multi_repo_mode: chaos
`)
	out, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err == nil {
		t.Fatal("expected mode error")
	}
	if !strings.Contains(out, "all/primary") {
		t.Fatalf("expected hint: %q", out)
	}
}

func TestWorkspaceValidateUnknownKeyIsWarning(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `id: demo
name: Demo
state: active
created_at: "2026-05-08T00:00:00Z"
some_future_field: nope
`)
	out, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err != nil {
		t.Fatalf("warning shouldn't fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[warning]") || !strings.Contains(out, "some_future_field") {
		t.Fatalf("expected warning for unknown key: %q", out)
	}
}

func TestWorkspaceValidateBadReposEntry(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `id: demo
name: Demo
state: active
created_at: "2026-05-08T00:00:00Z"
repos:
  - name: BadName
    path: src
`)
	_, err := executeCommand(t, "workspace", "validate", "--workspace", root)
	if err == nil {
		t.Fatal("expected repos error")
	}
}

func TestWorkspaceValidateJSON(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	writeWorkspaceYAML(t, root, `name: Demo
state: active
created_at: "2026-05-08T00:00:00Z"
`)
	out, err := executeCommand(t, "workspace", "validate", "--workspace", root, "--json")
	if err == nil {
		t.Fatal("expected error (missing id)")
	}
	// cobra appends "Error: ..." to the same buffer as our JSON
	// payload on error; isolate the JSON line.
	jsonLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "[") {
			jsonLine = line
			break
		}
	}
	var issues []map[string]any
	if jerr := json.Unmarshal([]byte(jsonLine), &issues); jerr != nil {
		t.Fatalf("json parse: %v\n%s", jerr, out)
	}
	if len(issues) == 0 {
		t.Fatalf("expected issues, got none")
	}
	if issues[0]["severity"] != "error" {
		t.Fatalf("first issue not error: %v", issues[0])
	}
}
