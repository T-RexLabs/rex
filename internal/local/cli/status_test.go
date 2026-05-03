package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusReportsWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "demo", "--name", "Demo"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Drop a fake spec, hook, and schedule so the counts are non-zero.
	specsDir := filepath.Join(dir, ".rex", "specs")
	if err := os.WriteFile(filepath.Join(specsDir, "first.yaml"), []byte("spec_version: 1\nmetadata: {id: f, name: F, state: draft}\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	hookPath := filepath.Join(dir, ".rex", "hooks", "post-spec-validate")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	schedulePath := filepath.Join(dir, ".rex", "schedules", "nightly.yaml")
	if err := os.WriteFile(schedulePath, []byte("# nightly\n"), 0o644); err != nil {
		t.Fatalf("write schedule: %v", err)
	}

	out, err := executeCommand(t, "status", "--workspace", dir)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{"demo", "Demo", "specs:       1", "hooks:       1", "schedules:   1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q\n%s", want, out)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "j", "--name", "J"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "status", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if v["id"] != "j" || v["state"] != "active" {
		t.Fatalf("status JSON missing fields: %v", v)
	}
}

func TestStatusReportsZeroRemotesByDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "wr", "--name", "WR"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := executeCommand(t, "status", "--workspace", dir)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "remotes:     none") {
		t.Fatalf("expected 'remotes: none' line: %s", out)
	}
}

func TestStatusListsRemotesWithDraftCounts(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "rs", "--name", "RS"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Push the workspace.created event to seed a watermark.
	if _, err := executeCommand(t, "push",
		"--workspace", dir, "--url", hs.URL, "--remote", "primary",
	); err != nil {
		t.Fatalf("push: %v", err)
	}
	// Run a shell command so there are drafts past the watermark.
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir, "--shell", "true", "--run-id", "rs-run",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}

	out, err := executeCommand(t, "status", "--workspace", dir)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "remotes:") {
		t.Fatalf("missing remotes section: %s", out)
	}
	if !strings.Contains(out, "primary") {
		t.Fatalf("missing primary remote line: %s", out)
	}
	// 4 draft events from `run start` (run.started + node.started +
	// node.succeeded + run.completed).
	if !strings.Contains(out, "4") {
		t.Fatalf("expected 4 drafts: %s", out)
	}
}

func TestStatusJSONRemotesIsArray(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "rj", "--name", "RJ"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := executeCommand(t, "push",
		"--workspace", dir, "--url", hs.URL, "--remote", "primary",
	); err != nil {
		t.Fatalf("push: %v", err)
	}

	out, err := executeCommand(t, "status", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	remotes, ok := v["remotes"].([]any)
	if !ok {
		t.Fatalf("remotes should be []any, got %T (%v)", v["remotes"], v["remotes"])
	}
	if len(remotes) != 1 {
		t.Fatalf("remotes len: got %d want 1 (%v)", len(remotes), remotes)
	}
	r0 := remotes[0].(map[string]any)
	if r0["name"] != "primary" {
		t.Fatalf("remote[0].name: got %v", r0["name"])
	}
	if r0["drafts"].(float64) != 0 {
		t.Fatalf("remote[0].drafts: got %v want 0", r0["drafts"])
	}
}

func TestStatusFailsWithoutWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() // no .rex/
	_, err := executeCommand(t, "status", "--workspace", dir)
	if err == nil {
		t.Fatal("expected error when no workspace exists at --workspace path")
	}
}
