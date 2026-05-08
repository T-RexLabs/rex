package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/audit"
)

// readSettingsState reads .rex/workspace.yaml's state field directly
// off disk so tests assert against bytes, not against the cached
// settings struct returned by readWorkspaceSettings.
func readSettingsState(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "state:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "state:"))
		}
	}
	return ""
}

func TestWorkspaceArchiveAndUnarchive(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if got := readSettingsState(t, root); got != "active" {
		t.Fatalf("initial state: %q", got)
	}

	// archive
	out, err := executeCommand(t, "workspace", "archive", "--workspace", root)
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	if !strings.Contains(out, "active -> archived") {
		t.Fatalf("output: %q", out)
	}
	if got := readSettingsState(t, root); got != "archived" {
		t.Fatalf("post-archive state: %q", got)
	}

	// archive again — idempotent no-op
	out, err = executeCommand(t, "workspace", "archive", "--workspace", root)
	if err != nil {
		t.Fatalf("archive idempotent: %v", err)
	}
	if !strings.Contains(out, "already archived") {
		t.Fatalf("idempotent output: %q", out)
	}

	// unarchive
	out, err = executeCommand(t, "workspace", "unarchive", "--workspace", root)
	if err != nil {
		t.Fatalf("unarchive: %v\n%s", err, out)
	}
	if !strings.Contains(out, "archived -> active") {
		t.Fatalf("unarchive output: %q", out)
	}
	if got := readSettingsState(t, root); got != "active" {
		t.Fatalf("post-unarchive state: %q", got)
	}

	// audit trail: one .archived + one .unarchived
	if got := readAuditEvents(t, root, audit.EventTypeWorkspaceArchived); len(got) != 1 {
		t.Fatalf("workspace.archived count: %d", len(got))
	}
	if got := readAuditEvents(t, root, audit.EventTypeWorkspaceUnarchived); len(got) != 1 {
		t.Fatalf("workspace.unarchived count: %d", len(got))
	}
}

func TestWorkspaceDeleteFromActive(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	out, err := executeCommand(t, "workspace", "delete", "--workspace", root)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(out, "active -> deleted") {
		t.Fatalf("output: %q", out)
	}
	if got := readSettingsState(t, root); got != "deleted" {
		t.Fatalf("state: %q", got)
	}

	events := readAuditEvents(t, root, audit.EventTypeWorkspaceDeleted)
	if len(events) != 1 {
		t.Fatalf("want 1 workspace.deleted, got %d", len(events))
	}
	var p audit.WorkspaceStateChangedEvent
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.From != "active" || p.To != "deleted" {
		t.Fatalf("payload: %+v", p)
	}
}

func TestWorkspaceDeleteFromArchived(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "workspace", "archive", "--workspace", root); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := executeCommand(t, "workspace", "delete", "--workspace", root); err != nil {
		t.Fatalf("delete from archived: %v", err)
	}
	if got := readSettingsState(t, root); got != "deleted" {
		t.Fatalf("state: %q", got)
	}
}

func TestWorkspaceUnarchiveAfterDeleteRefuses(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "workspace", "delete", "--workspace", root); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := executeCommand(t, "workspace", "unarchive", "--workspace", root)
	if err == nil {
		t.Fatal("expected refusal for unarchive after delete")
	}
	if !strings.Contains(err.Error(), "snapshot restore") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestWorkspaceLifecyclePreservesUnknownFields(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	wsPath := filepath.Join(root, ".rex", "workspace.yaml")
	body, err := os.ReadFile(wsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body = append(body, []byte("default_repo: my-favourite\n")...)
	if err := os.WriteFile(wsPath, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := executeCommand(t, "workspace", "archive", "--workspace", root); err != nil {
		t.Fatalf("archive: %v", err)
	}

	body, err = os.ReadFile(wsPath)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !strings.Contains(string(body), "default_repo: my-favourite") {
		t.Fatalf("yaml.Node round-trip dropped unknown field; file:\n%s", body)
	}
	if !strings.Contains(string(body), "state: archived") {
		t.Fatalf("state not flipped; file:\n%s", body)
	}
}

func TestWorkspaceUnarchiveNoOpWhenActive(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	out, err := executeCommand(t, "workspace", "unarchive", "--workspace", root)
	if err != nil {
		t.Fatalf("unarchive on active: %v", err)
	}
	if !strings.Contains(out, "already active") {
		t.Fatalf("output: %q", out)
	}
}
