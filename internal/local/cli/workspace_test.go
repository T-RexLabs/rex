package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

func TestWorkspaceInitCreatesSkeleton(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out, err := executeCommand(t, "workspace", "init", dir)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Initialized rex workspace") {
		t.Fatalf("output missing confirmation: %s", out)
	}

	// .rex/ + sub-dirs exist
	for _, p := range []string{"", "specs", "schedules", "templates", "hooks"} {
		full := filepath.Join(dir, ".rex", p)
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected directory %s: err=%v", full, err)
		}
	}

	// workspace.yaml is well-formed
	body, err := os.ReadFile(filepath.Join(dir, ".rex", "workspace.yaml"))
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	var s workspaceSettings
	if err := yaml.Unmarshal(body, &s); err != nil {
		t.Fatalf("yaml parse: %v", err)
	}
	if s.ID == "" || s.Name == "" || s.State != "active" || s.CreatedAt == "" {
		t.Fatalf("workspace settings missing fields: %+v", s)
	}
}

func TestWorkspaceInitRefusesExistingWithoutForce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir); err != nil {
		t.Fatalf("first init: %v", err)
	}
	_, err := executeCommand(t, "workspace", "init", dir)
	if err == nil {
		t.Fatal("second init without --force should fail")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestWorkspaceInitForceOverwrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if _, err := executeCommand(t, "workspace", "init", dir, "--force", "--name", "renamed"); err != nil {
		t.Fatalf("force init: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".rex", "workspace.yaml"))
	if !strings.Contains(string(body), "renamed") {
		t.Fatalf("force did not rewrite workspace.yaml: %s", body)
	}
}

func TestWorkspaceInitRejectsBadID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := executeCommand(t, "workspace", "init", dir, "--id", "Not Kebab")
	if err == nil {
		t.Fatal("expected kebab-case rejection")
	}
}

func TestWorkspaceInitDerivesIDFromBasename(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dir := filepath.Join(parent, "My-Cool Workspace!")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := executeCommand(t, "workspace", "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".rex", "workspace.yaml"))
	var s workspaceSettings
	_ = yaml.Unmarshal(body, &s)
	if s.ID != "my-cool-workspace" {
		t.Fatalf("id derivation: got %q want my-cool-workspace", s.ID)
	}
}

func TestWorkspaceShowReadsBack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "demo", "--name", "Demo"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "workspace", "show", dir)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	for _, want := range []string{"demo", "Demo", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q\n%s", want, out)
		}
	}
}

func TestWorkspaceShowJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "j", "--name", "J"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "workspace", "show", dir, "--json")
	if err != nil {
		t.Fatalf("show --json: %v\n%s", err, out)
	}
	// Single JSON object on stdout.
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if v["id"] != "j" {
		t.Fatalf("json id: %v", v["id"])
	}
}

func TestWorkspaceShowFailsWhenAbsent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() // no .rex/ here
	_, err := executeCommand(t, "workspace", "show", dir)
	if err == nil {
		t.Fatal("expected error when no workspace exists")
	}
}

func TestWorkspaceInitEmitsWorkspaceCreatedAuditEvent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "demo", "--name", "Demo Workspace"); err != nil {
		t.Fatalf("init: %v", err)
	}

	r, err := eventlog.OpenReader(filepath.Join(dir, ".rex", "events.log"))
	if err != nil {
		t.Fatalf("open events.log: %v", err)
	}
	defer r.Close()

	rec, err := r.Next()
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if rec.Type != audit.EventTypeWorkspaceCreated {
		t.Fatalf("event type: got %q want %q", rec.Type, audit.EventTypeWorkspaceCreated)
	}
	if rec.WorkspaceID != "demo" {
		t.Fatalf("workspace id on record: got %q", rec.WorkspaceID)
	}

	var body audit.WorkspaceCreatedEvent
	if err := json.Unmarshal(rec.Payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if body.WorkspaceID != "demo" || body.Name != "Demo Workspace" {
		t.Fatalf("payload fields: %+v", body)
	}
	if body.Path == "" || !strings.HasSuffix(body.Path, dir) {
		t.Fatalf("payload path: %q (want suffix %q)", body.Path, dir)
	}
	if body.CreatedAt == "" {
		t.Fatal("payload created_at empty")
	}

	// One and only one event from init.
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after one record, got %v", err)
	}
}

func TestWorkspaceListPlaceholder(t *testing.T) {
	t.Parallel()

	// list cannot reasonably operate on cwd in parallel-safe tests
	// because it uses os.Getwd(); we just exercise the help path here.
	out, err := executeCommand(t, "workspace", "list", "--help")
	if err != nil {
		t.Fatalf("list --help: %v", err)
	}
	if !strings.Contains(out, "registry") {
		t.Fatalf("list help should mention registry: %s", out)
	}
}
