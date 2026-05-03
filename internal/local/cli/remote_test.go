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
		"--remotes-file", reg,
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
	if _, err := executeCommand(t, "remote", "add", "primary", "http://x", "--remotes-file", reg); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := executeCommand(t, "remote", "add", "primary", "http://y", "--remotes-file", reg); err == nil {
		t.Fatal("duplicate add should error")
	}
}

func TestRemoteAddRejectsBadName(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "add", "Bad Name", "http://x", "--remotes-file", reg)
	if err == nil {
		t.Fatal("expected error for bad name")
	}
}

func TestRemoteRemove(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "primary", "http://x", "--remotes-file", reg); err != nil {
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
	if _, err := executeCommand(t, "remote", "add", "alpha", "http://a", "--remotes-file", reg); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if _, err := executeCommand(t, "remote", "add", "beta", "http://b", "--remotes-file", reg); err != nil {
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

func TestRemoteTestRecordsFingerprint(t *testing.T) {
	t.Parallel()

	srv, hs := startCentral(t)
	reg := tempRegistry(t)

	if _, err := executeCommand(t, "remote", "add", "primary", hs.URL, "--remotes-file", reg); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := executeCommand(t, "remote", "test", "primary", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("test: %v\n%s", err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK: %s", out)
	}
	// Fingerprint should now be recorded; show reflects it.
	show, err := executeCommand(t, "remote", "show", "primary", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, show)
	}
	if !strings.Contains(show, srv.Actor().Fingerprint.String()) {
		t.Fatalf("show should include observed fingerprint: %s", show)
	}
}

func TestPushUsesNamedRemoteFromRegistry(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "primary", hs.URL, "--remotes-file", reg); err != nil {
		t.Fatalf("add: %v", err)
	}
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "ru", "--name", "RU"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "push",
		"--workspace", dir,
		"--remote", "primary",
		"--remotes-file", reg,
	)
	if err != nil {
		t.Fatalf("push by name: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pushed 1") {
		t.Fatalf("expected push to succeed: %s", out)
	}
}

func TestPushUnknownRemoteWithoutURL(t *testing.T) {
	t.Parallel()

	reg := tempRegistry(t)
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "rn", "--name", "RN"); err != nil {
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

func TestPushURLOverridesRegistry(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	reg := tempRegistry(t)
	// Register with a bogus URL; --url should still win.
	if _, err := executeCommand(t, "remote", "add", "primary", "http://bogus.invalid:1",
		"--remotes-file", reg,
	); err != nil {
		t.Fatalf("add: %v", err)
	}
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "ov", "--name", "OV"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "push",
		"--workspace", dir,
		"--url", hs.URL,
		"--remotes-file", reg,
	)
	if err != nil {
		t.Fatalf("push --url: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pushed 1") {
		t.Fatalf("expected push to succeed with --url override: %s", out)
	}
}
