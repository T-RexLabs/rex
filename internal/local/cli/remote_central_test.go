//go:build central_e2e

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// These remote tests exercise the handshake/push paths against a live
// central node and need the parked central, so they live behind the
// `central_e2e` build tag. The registry-only remote tests (add/list/
// show/remove/validation) stay in remote_test.go in the default suite.
// They share the tempRegistry + executeCommand* helpers defined there.

func executeCommandWithStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestRemoteTestRecordsFingerprint(t *testing.T) {
	t.Parallel()

	srv, hs := startCentral(t)
	reg := tempRegistry(t)

	if _, err := executeCommand(t, "remote", "add", "primary", hs.URL, "--remotes-file", reg, "--yes"); err != nil {
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

// TestRemoteAddDoesHandshakeAndRecordsFingerprint covers
// sync.BOOT.1: by default `rex remote add` contacts the server,
// fetches its fingerprint, and records it on the registry entry.
// --yes accepts the observed fingerprint without prompting.
func TestRemoteAddDoesHandshakeAndRecordsFingerprint(t *testing.T) {
	t.Parallel()
	srv, hs := startCentral(t)
	reg := tempRegistry(t)

	out, err := executeCommand(t, "remote", "add", "primary", hs.URL,
		"--remotes-file", reg, "--yes",
	)
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "server fingerprint:") {
		t.Errorf("output missing fingerprint preamble: %s", out)
	}
	show, err := executeCommand(t, "remote", "show", "primary", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, show)
	}
	if !strings.Contains(show, srv.Actor().Fingerprint.String()) {
		t.Errorf("show should include fingerprint stamped at add time: %s", show)
	}
}

// TestRemoteAddPromptAcceptsViaPipedYes covers BOOT.1.1's
// confirmation prompt: a "y\n" piped via stdin counts as accept.
func TestRemoteAddPromptAcceptsViaPipedYes(t *testing.T) {
	t.Parallel()
	_, hs := startCentral(t)
	reg := tempRegistry(t)

	out, err := executeCommandWithStdin(t, "y\n", "remote", "add", "primary", hs.URL,
		"--remotes-file", reg,
	)
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, `added remote "primary"`) {
		t.Errorf("output missing add confirmation: %s", out)
	}
}

// TestRemoteAddDeclinesOnEmptyStdin covers the non-interactive
// "stdin EOF" branch: confirmTrust returns false, the command
// reports decline and exits 0 without registering.
func TestRemoteAddDeclinesOnEmptyStdin(t *testing.T) {
	t.Parallel()
	_, hs := startCentral(t)
	reg := tempRegistry(t)

	out, err := executeCommandWithStdin(t, "", "remote", "add", "primary", hs.URL,
		"--remotes-file", reg,
	)
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("output missing decline message: %s", out)
	}
	// Registry should be empty.
	list, _ := executeCommand(t, "remote", "list", "--remotes-file", reg)
	if !strings.Contains(list, "no remotes registered") {
		t.Errorf("registry should be empty after decline: %s", list)
	}
}

func TestPushUsesNamedRemoteFromRegistry(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "primary", hs.URL, "--remotes-file", reg, "--yes"); err != nil {
		t.Fatalf("add: %v", err)
	}
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir, "--id", "ru", "--name", "RU"); err != nil {
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

func TestPushURLOverridesRegistry(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	reg := tempRegistry(t)
	// Register with a bogus URL; --url should still win.
	if _, err := executeCommand(t, "remote", "add", "primary", "http://bogus.invalid:1",
		"--remotes-file", reg, "--skip-handshake",
	); err != nil {
		t.Fatalf("add: %v", err)
	}
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir, "--id", "ov", "--name", "OV"); err != nil {
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
