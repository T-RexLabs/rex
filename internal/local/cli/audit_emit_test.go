package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/audit"
)

func TestSpecCreateEmitsAuditEvent(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "spec", "create", "--workspace", root, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	events := readAuditEvents(t, root, audit.EventTypeSpecCreated)
	if len(events) != 1 {
		t.Fatalf("want 1 spec.created, got %d", len(events))
	}
	var p audit.SpecCreatedEvent
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.SpecID != "demo" || !strings.HasSuffix(p.Path, "/demo.yaml") {
		t.Fatalf("payload: %+v", p)
	}
	if p.WorkspaceID == "" {
		t.Fatalf("workspace_id missing: %+v", p)
	}
}

func TestSpecEditEmitsAuditEvent(t *testing.T) {
	// Not t.Parallel(): the fake-editor fixture mutates $EDITOR.
	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "spec", "create", "--workspace", root, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	cleanup := installFakeEditor(t, "\n# audit-emit test\n")
	defer cleanup()

	if _, err := executeCommand(t, "spec", "edit", "--workspace", root, "demo"); err != nil {
		t.Fatalf("spec edit: %v", err)
	}

	events := readAuditEvents(t, root, audit.EventTypeSpecEdited)
	if len(events) != 1 {
		t.Fatalf("want 1 spec.edited, got %d", len(events))
	}
	var p audit.SpecEditedEvent
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.SpecID != "demo" || p.HasErrors {
		t.Fatalf("payload: %+v", p)
	}
}

func TestSpecEditEmitsAuditEventEvenOnValidationFailure(t *testing.T) {
	root := initWorkspace(t, t.TempDir())
	if _, err := executeCommand(t, "spec", "create", "--workspace", root, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	cleanup := installFakeEditor(t, "\nunknown_top_level_key: nope\n")
	defer cleanup()

	if _, err := executeCommand(t, "spec", "edit", "--workspace", root, "demo"); err == nil {
		t.Fatal("expected validation error")
	}

	events := readAuditEvents(t, root, audit.EventTypeSpecEdited)
	if len(events) != 1 {
		t.Fatalf("want 1 spec.edited, got %d", len(events))
	}
	var p audit.SpecEditedEvent
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !p.HasErrors {
		t.Fatalf("HasErrors should be true on validation failure: %+v", p)
	}
}

func TestRemoteAddRemoveEmitAuditWhenInWorkspace(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	remotesPath := filepath.Join(t.TempDir(), "remotes.toml")

	if _, err := executeCommand(t, "remote", "--workspace", root,
		"add", "primary", "https://example.invalid",
		"--remotes-file", remotesPath, "--skip-handshake",
	); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	added := readAuditEvents(t, root, audit.EventTypeRemoteAttached)
	if len(added) != 1 {
		t.Fatalf("want 1 remote.attached, got %d", len(added))
	}
	var p audit.RemoteAttachedEvent
	if err := json.Unmarshal(added[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Name != "primary" || p.URL != "https://example.invalid" {
		t.Fatalf("payload: %+v", p)
	}

	if _, err := executeCommand(t, "remote", "--workspace", root,
		"remove", "primary",
		"--remotes-file", remotesPath,
	); err != nil {
		t.Fatalf("remote remove: %v", err)
	}
	removed := readAuditEvents(t, root, audit.EventTypeRemoteDetached)
	if len(removed) != 1 {
		t.Fatalf("want 1 remote.detached, got %d", len(removed))
	}
}
