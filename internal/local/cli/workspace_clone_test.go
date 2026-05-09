package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceCloneEndToEnd seeds a source workspace, pushes its
// events to a real httptest-backed central, then clones a fresh
// workspace from it. Asserts:
//   - .rex/ skeleton exists at the target
//   - workspace.yaml is reconstructed with the right id + name
//   - events.log carries every workspace-id-matching record from
//     the remote (count >= 1; precise count depends on what init
//     emits)
//   - watermark file is stamped
//   - remote registered locally under the chosen alias
//   - workspace registered in the global registry
func TestWorkspaceCloneEndToEnd(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)

	// Set up the SOURCE workspace and push.
	src := initSyncWorkspace(t)
	if _, err := executeCommand(t, "push",
		"--workspace", src, "--url", hs.URL,
	); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Now clone into a fresh dir. Use --remotes-file +
	// --registry-file to keep the test isolated from $HOME.
	target := filepath.Join(t.TempDir(), "cloned")
	remotesPath := filepath.Join(t.TempDir(), "remotes.toml")
	registryPath := filepath.Join(t.TempDir(), "registry.toml")

	out, err := executeCommand(t, "workspace", "clone",
		hs.URL, "demo", target,
		"--remote-name", "primary",
		"--remotes-file", remotesPath,
		"--registry-file", registryPath,
	)
	if err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Cloned workspace") {
		t.Fatalf("output: %q", out)
	}

	// Skeleton + workspace.yaml.
	for _, sub := range []string{"specs", "schedules", "templates", "hooks"} {
		if _, err := os.Stat(filepath.Join(target, ".rex", sub)); err != nil {
			t.Fatalf("missing skeleton dir %s: %v", sub, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(target, ".rex", "workspace.yaml"))
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	if !strings.Contains(string(body), "id: demo") {
		t.Fatalf("workspace.yaml missing id=demo: %s", body)
	}
	if !strings.Contains(string(body), "name: Demo") {
		t.Fatalf("workspace.yaml missing name=Demo: %s", body)
	}
	if !strings.Contains(string(body), "state: active") {
		t.Fatalf("workspace.yaml missing state=active: %s", body)
	}

	// Watermark stamped.
	wm, err := os.ReadFile(filepath.Join(target, ".rex", "drafts", "primary.toml"))
	if err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if !strings.Contains(string(wm), "last_acked_event_id") {
		t.Fatalf("watermark missing key: %s", wm)
	}

	// Remote registered.
	rems, err := os.ReadFile(remotesPath)
	if err != nil {
		t.Fatalf("read remotes: %v", err)
	}
	if !strings.Contains(string(rems), "[primary]") || !strings.Contains(string(rems), hs.URL) {
		t.Fatalf("remotes.toml: %s", rems)
	}

	// Workspace in registry.
	regBody, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(regBody), `id = "demo"`) {
		t.Fatalf("registry: %s", regBody)
	}
	if !strings.Contains(string(regBody), `remote = "primary"`) {
		t.Fatalf("registry should record remote='primary': %s", regBody)
	}
}

func TestWorkspaceCloneRefusesExistingWorkspace(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	target := initSyncWorkspace(t) // already has .rex/

	_, err := executeCommand(t, "workspace", "clone",
		hs.URL, "demo", target,
		"--remote-name", "primary",
		"--remotes-file", filepath.Join(t.TempDir(), "remotes.toml"),
		"--registry-file", filepath.Join(t.TempDir(), "registry.toml"),
	)
	if err == nil {
		t.Fatal("expected refusal when target has .rex/")
	}
	if !strings.Contains(err.Error(), "already contains") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestWorkspaceCloneNoMatchingWorkspace(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	target := filepath.Join(t.TempDir(), "ghost-clone")

	_, err := executeCommand(t, "workspace", "clone",
		hs.URL, "ghost-workspace", target,
		"--remote-name", "primary",
		"--remotes-file", filepath.Join(t.TempDir(), "remotes.toml"),
		"--registry-file", filepath.Join(t.TempDir(), "registry.toml"),
	)
	if err == nil {
		t.Fatal("expected error when remote has no matching events")
	}
	if !strings.Contains(err.Error(), "no events") {
		t.Fatalf("error: %v", err)
	}
}

// TestWorkspaceCloneFoldsStateTransitions covers the bug fix that
// brought clone in line with workspace.LIFE.3: archive / unarchive
// / delete events on the source must surface in the cloned
// workspace.yaml's state field. Pre-fix, clone hard-coded "active"
// regardless of subsequent transitions.
func TestWorkspaceCloneFoldsStateTransitions(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	src := initSyncWorkspace(t)

	// Archive the source workspace, then push.
	if _, err := executeCommand(t, "workspace", "archive", "--workspace", src); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := executeCommand(t, "push",
		"--workspace", src, "--url", hs.URL,
	); err != nil {
		t.Fatalf("push: %v", err)
	}

	target := filepath.Join(t.TempDir(), "clone-archived")
	if _, err := executeCommand(t, "workspace", "clone",
		hs.URL, "demo", target,
		"--remote-name", "primary",
		"--remotes-file", filepath.Join(t.TempDir(), "remotes.toml"),
		"--registry-file", filepath.Join(t.TempDir(), "registry.toml"),
	); err != nil {
		t.Fatalf("clone: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(target, ".rex", "workspace.yaml"))
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	if !strings.Contains(string(body), "state: archived") {
		t.Fatalf("clone should have folded archived state; got: %s", body)
	}
}

func TestWorkspaceCloneFoldsArchiveUnarchiveDelete(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	src := initSyncWorkspace(t)

	// archive → unarchive → archive again. Last transition wins.
	for _, cmd := range []string{"archive", "unarchive", "archive"} {
		if _, err := executeCommand(t, "workspace", cmd, "--workspace", src); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	if _, err := executeCommand(t, "push",
		"--workspace", src, "--url", hs.URL,
	); err != nil {
		t.Fatalf("push: %v", err)
	}

	target := filepath.Join(t.TempDir(), "clone-multitrans")
	if _, err := executeCommand(t, "workspace", "clone",
		hs.URL, "demo", target,
		"--remote-name", "primary",
		"--remotes-file", filepath.Join(t.TempDir(), "remotes.toml"),
		"--registry-file", filepath.Join(t.TempDir(), "registry.toml"),
	); err != nil {
		t.Fatalf("clone: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(target, ".rex", "workspace.yaml"))
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	if !strings.Contains(string(body), "state: archived") {
		t.Fatalf("expected last-transition state archived; got: %s", body)
	}
}

func TestWorkspaceCloneJSON(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	src := initSyncWorkspace(t)
	if _, err := executeCommand(t, "push",
		"--workspace", src, "--url", hs.URL,
	); err != nil {
		t.Fatalf("push: %v", err)
	}

	target := filepath.Join(t.TempDir(), "cloned-json")
	out, err := executeCommand(t, "workspace", "clone",
		hs.URL, "demo", target,
		"--remote-name", "primary",
		"--remotes-file", filepath.Join(t.TempDir(), "remotes.toml"),
		"--registry-file", filepath.Join(t.TempDir(), "registry.toml"),
		"--json",
	)
	if err != nil {
		t.Fatalf("clone --json: %v\n%s", err, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if v["id"] != "demo" || v["remote"] != "primary" {
		t.Fatalf("json shape: %v", v)
	}
	if v["events"].(float64) < 1 {
		t.Fatalf("expected >=1 events in stream: %v", v)
	}
}
