package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func tempRegistry(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "remotes.toml")
}

func TestRemoteAddListShow(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)

	if _, err := executeCommand(t, "remote", "add", "primary", "http://127.0.0.1:9000",
		"--remotes-file", reg, "--skip-handshake",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, err := executeCommand(t, "remote", "list", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "primary") || !strings.Contains(out, "127.0.0.1:9000") {
		t.Fatalf("list missing entry: %s", out)
	}

	out, err = executeCommand(t, "remote", "show", "primary", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "url:         http://127.0.0.1:9000") {
		t.Fatalf("show missing url: %s", out)
	}
}

func TestRemoteAddRejectsDuplicate(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "primary", "http://x", "--remotes-file", reg, "--skip-handshake"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := executeCommand(t, "remote", "add", "primary", "http://y", "--remotes-file", reg, "--skip-handshake"); err == nil {
		t.Fatal("duplicate add should error")
	}
}

func TestRemoteAddRejectsBadName(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "add", "Bad Name", "http://x", "--remotes-file", reg, "--skip-handshake")
	if err == nil {
		t.Fatal("expected error for bad name")
	}
}

func TestRemoteRemove(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "primary", "http://x", "--remotes-file", reg, "--skip-handshake"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := executeCommand(t, "remote", "remove", "primary", "--remotes-file", reg); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out, err := executeCommand(t, "remote", "list", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if !strings.Contains(out, "no remotes registered") {
		t.Fatalf("expected empty list: %s", out)
	}
}

func TestRemoteShowMissing(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "show", "ghost", "--remotes-file", reg)
	if err == nil {
		t.Fatal("show on missing remote should error")
	}
}

func TestRemoteListJSON(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "alpha", "http://a", "--remotes-file", reg, "--skip-handshake"); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if _, err := executeCommand(t, "remote", "add", "beta", "http://b", "--remotes-file", reg, "--skip-handshake"); err != nil {
		t.Fatalf("add beta: %v", err)
	}
	out, err := executeCommand(t, "remote", "list", "--remotes-file", reg, "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	var v []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out)
	}
	if len(v) != 2 {
		t.Fatalf("len: got %d want 2: %v", len(v), v)
	}
	if v[0]["Name"] != "alpha" {
		t.Fatalf("first entry: %v", v[0])
	}
}

// TestRemoteAddRejectsURLWithPath covers the friendlier
// fail-fast on a per-page URL like /orgs/<id>: the command
// errors before any network call, telling the user exactly
// what to fix (use the base URL).
func TestRemoteAddRejectsURLWithPath(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "add", "primary",
		"http://127.0.0.1:8080/orgs/c7d6eb43-22fc-4c8f-9a14-8ba4695d8257",
		"--remotes-file", reg, "--yes",
	)
	if err == nil {
		t.Fatal("expected URL-with-path to error before handshake")
	}
	if !strings.Contains(err.Error(), "use the central node's base URL") {
		t.Fatalf("error wording missing base-URL hint: %v", err)
	}
	if !strings.Contains(err.Error(), `"http://127.0.0.1:8080"`) {
		t.Fatalf("error didn't suggest the stripped base URL: %v", err)
	}
}

// TestRemoteAddRejectsMissingScheme covers the no-scheme
// branch: a bare host:port fails fast with a clear error
// instead of "Get : unsupported protocol scheme".
func TestRemoteAddRejectsMissingScheme(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "add", "primary",
		"127.0.0.1:8080",
		"--remotes-file", reg, "--yes",
	)
	if err == nil {
		t.Fatal("expected scheme-less URL to error")
	}
	if !strings.Contains(err.Error(), "http:// or https://") {
		t.Fatalf("error wording missing scheme hint: %v", err)
	}
}

// TestRemoteAddNetworkFailureLeavesRegistryUntouched covers the
// "handshake failed" branch: an unreachable URL errors out and
// the registry stays empty.
func TestRemoteAddNetworkFailureLeavesRegistryUntouched(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "add", "primary", "http://127.0.0.1:1",
		"--remotes-file", reg, "--yes",
	)
	if err == nil {
		t.Fatal("expected handshake to fail against unreachable URL")
	}
	if !strings.Contains(err.Error(), "contact") || !strings.Contains(err.Error(), "--skip-handshake") {
		t.Errorf("error wording: %v", err)
	}
	list, _ := executeCommand(t, "remote", "list", "--remotes-file", reg)
	if !strings.Contains(list, "no remotes registered") {
		t.Errorf("registry should be empty after handshake failure: %s", list)
	}
}

func TestRemoteBootstrapRequiresToken(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "bootstrap", "primary", "https://example.invalid", "--remotes-file", reg)
	if err == nil {
		t.Fatal("bootstrap without token should error")
	}
	if !strings.Contains(err.Error(), "required flag") || !strings.Contains(err.Error(), "token") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestPushUnknownRemoteWithoutURL(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir, "--id", "rn", "--name", "RN"); err != nil {
		t.Fatalf("init: %v", err)
	}
	_, err := executeCommand(t, "push",
		"--workspace", dir,
		"--remote", "ghost",
		"--remotes-file", reg,
	)
	if err == nil {
		t.Fatal("push against unregistered remote without --url should error")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error wording: %v", err)
	}
}
