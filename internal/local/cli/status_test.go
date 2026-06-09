package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The remote-aware status tests (draft counts, remotes JSON array,
// rebase-needed flag) need the parked central node and live in
// status_central_test.go behind the `central_e2e` build tag. The
// pure-local status tests below stay in the default suite.

func TestStatusReportsWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir, "--id", "demo", "--name", "Demo"); err != nil {
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
	if _, err := executeCommand(t, "init", dir, "--id", "j", "--name", "J"); err != nil {
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
	if _, err := executeCommand(t, "init", dir, "--id", "wr", "--name", "WR"); err != nil {
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

func TestStatusFailsWithoutWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() // no .rex/
	_, err := executeCommand(t, "status", "--workspace", dir)
	if err == nil {
		t.Fatal("expected error when no workspace exists at --workspace path")
	}
}
