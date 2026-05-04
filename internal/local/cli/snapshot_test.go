package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initSnapshotWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "snap", "--name", "Snap"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	// Drop a spec into specs/ so create has something interesting
	// to capture.
	specPath := filepath.Join(dir, ".rex", "specs", "alpha.yaml")
	if err := os.WriteFile(specPath, []byte("original spec body"), 0o644); err != nil {
		t.Fatalf("seed spec: %v", err)
	}
	return dir
}

func TestSnapshotCreateAndList(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)

	out, err := executeCommand(t, "snapshot", "create", "--workspace", dir)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "snapshot") || !strings.Contains(out, "created") {
		t.Fatalf("create output: %s", out)
	}

	listOut, err := executeCommand(t, "snapshot", "list", "--workspace", dir)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "SNAPSHOT_ID") {
		t.Fatalf("list missing header: %s", listOut)
	}
	if strings.Contains(listOut, "no snapshots yet") {
		t.Fatalf("list reported empty after create: %s", listOut)
	}
}

func TestSnapshotListEmpty(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)
	out, err := executeCommand(t, "snapshot", "list", "--workspace", dir)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no snapshots yet") {
		t.Fatalf("expected empty: %s", out)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)

	// Create the snapshot.
	out, err := executeCommand(t, "snapshot", "create", "--workspace", dir, "--json")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out)
	}
	snapID, _ := m["SnapshotID"].(string)
	if snapID == "" {
		t.Fatalf("snapshot id missing: %v", m)
	}

	// Modify a spec post-snapshot.
	specPath := filepath.Join(dir, ".rex", "specs", "alpha.yaml")
	if err := os.WriteFile(specPath, []byte("dirty edits"), 0o644); err != nil {
		t.Fatalf("edit spec: %v", err)
	}

	// Add a spec that doesn't exist in the snapshot.
	addedPath := filepath.Join(dir, ".rex", "specs", "added.yaml")
	if err := os.WriteFile(addedPath, []byte("only post-snapshot"), 0o644); err != nil {
		t.Fatalf("add spec: %v", err)
	}

	// Restore.
	if _, err := executeCommand(t, "snapshot", "restore", snapID, "--workspace", dir); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if string(got) != "original spec body" {
		t.Fatalf("spec not restored: %q", got)
	}
	if _, err := os.Stat(addedPath); err == nil {
		t.Fatal("post-snapshot spec should be removed by restore")
	}
}

func TestSnapshotRestoreUnknownID(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)
	_, err := executeCommand(t, "snapshot", "restore", "ghost", "--workspace", dir)
	if err == nil {
		t.Fatal("restore on unknown id should error")
	}
}

func TestSnapshotPruneDryRun(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)
	for i := 0; i < 3; i++ {
		if _, err := executeCommand(t, "snapshot", "create", "--workspace", dir); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	out, err := executeCommand(t, "snapshot", "prune",
		"--workspace", dir,
		"--keep-last", "1",
		"--keep-monthly=false",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("prune --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would delete") {
		t.Fatalf("dry-run wording: %s", out)
	}

	// Confirm nothing was actually deleted.
	listOut, _ := executeCommand(t, "snapshot", "list", "--workspace", dir, "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(listOut)), &rows); err != nil {
		t.Fatalf("list JSON: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("dry-run should not delete; got %d rows", len(rows))
	}
}

func TestSnapshotPruneActuallyDeletes(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)
	for i := 0; i < 5; i++ {
		if _, err := executeCommand(t, "snapshot", "create", "--workspace", dir); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	out, err := executeCommand(t, "snapshot", "prune",
		"--workspace", dir,
		"--keep-last", "2",
		"--keep-monthly=false",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "deleted 3") {
		t.Fatalf("prune wording: %s", out)
	}

	listOut, _ := executeCommand(t, "snapshot", "list", "--workspace", dir, "--json")
	var rows []map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(listOut)), &rows)
	if len(rows) != 2 {
		t.Fatalf("after prune: got %d rows", len(rows))
	}
}

func TestSnapshotCreateFailsWithoutWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() // no .rex/
	_, err := executeCommand(t, "snapshot", "create", "--workspace", dir)
	if err == nil {
		t.Fatal("expected error: no workspace")
	}
}

func TestSnapshotCreateLeavesEventLogUntouched(t *testing.T) {
	t.Parallel()

	dir := initSnapshotWorkspace(t)
	logPath := filepath.Join(dir, ".rex", "events.log")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read events.log: %v", err)
	}

	if _, err := executeCommand(t, "snapshot", "create", "--workspace", dir); err != nil {
		t.Fatalf("create: %v", err)
	}

	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read events.log: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("create modified events.log:\n before %q\nafter %q", before, after)
	}
}
