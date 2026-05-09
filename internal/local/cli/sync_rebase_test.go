package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/central/server"
	"github.com/asabla/rex/internal/core/sync/conflict"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// seedGitOnCentral pushes content directly into the central git
// store so the rebase target has a known remote revision.
func seedGitOnCentral(t *testing.T, srv *server.Server, entity, content string) {
	t.Helper()
	rec := proto.GitEntity{
		Path: entity, Content: content, Revision: proto.GitContentRevision(content),
	}
	if err := srv.GitStore().Put(context.Background(), rec, ""); err != nil {
		t.Fatalf("seed git: %v", err)
	}
}

// writeRexFile writes <root>/.rex/<rel> with the given content,
// creating intermediate directories.
func writeRexFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, ".rex", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSyncRebaseLocalOnly(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)
	writeRexFile(t, dir, "specs/x.yaml", "spec_version: 1\n")

	out, err := executeCommand(t, "sync", "rebase", "specs/x.yaml",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("rebase: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no remote revision yet") {
		t.Fatalf("expected local-only message: %s", out)
	}
}

func TestSyncRebaseCleanMerge(t *testing.T) {
	t.Parallel()

	srv, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	base := "alpha\nbeta\n"
	local := "ALPHA\nbeta\n"
	remote := "alpha\nBETA\n"
	seedGitOnCentral(t, srv, "specs/m.yaml", remote)
	writeRexFile(t, dir, "specs/m.yaml", local)

	// Pre-populate the merge base via the sync package's saveBase
	// so the three-way merge has a real ancestor. Using the same
	// directory layout the rebase orchestrator does.
	baseDir := filepath.Join(dir, ".rex", "drafts", "primary.git")
	if err := os.MkdirAll(filepath.Join(baseDir, "specs"), 0o755); err != nil {
		t.Fatalf("mkdir base dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "specs", "m.yaml"), []byte(base), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}

	out, err := executeCommand(t, "sync", "rebase", "specs/m.yaml",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("rebase: %v\n%s", err, out)
	}
	if !strings.Contains(out, "merged cleanly") {
		t.Fatalf("expected clean merge message: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".rex", "specs", "m.yaml"))
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	want := "ALPHA\nBETA\n"
	if string(got) != want {
		t.Fatalf("merged file:\n%s\nwant:\n%s", got, want)
	}
}

func TestSyncRebaseConflictWritesSidecar(t *testing.T) {
	t.Parallel()

	srv, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	base := "name: original\n"
	local := "name: local-edit\n"
	remote := "name: remote-edit\n"
	seedGitOnCentral(t, srv, "workspace.yaml", remote)
	writeRexFile(t, dir, "workspace.yaml", local)
	if err := os.MkdirAll(filepath.Join(dir, ".rex", "drafts", "primary.git"), 0o755); err != nil {
		t.Fatalf("mkdir base dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".rex", "drafts", "primary.git", "workspace.yaml"), []byte(base), 0o644); err != nil {
		t.Fatalf("seed base: %v", err)
	}

	out, err := executeCommand(t, "sync", "rebase", "workspace.yaml",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("rebase: %v\n%s", err, out)
	}
	if !strings.Contains(out, "unresolved hunk") {
		t.Fatalf("expected unresolved hunks message: %s", out)
	}

	entityPath := filepath.Join(dir, ".rex", "workspace.yaml")
	side, err := conflict.Exists(entityPath)
	if err != nil || !side {
		t.Fatalf("expected sidecar; err=%v exists=%v", err, side)
	}
}

func TestSyncResolveClearsSidecar(t *testing.T) {
	t.Parallel()

	dir := initSyncWorkspace(t)
	writeRexFile(t, dir, "specs/x.yaml", "spec_version: 1\n# resolved by hand\n")
	// Pre-stamp a sidecar to simulate a prior failed rebase.
	side := conflict.Sidecar{
		Entity: "specs/x.yaml", Remote: "primary",
		LocalRevision: "rev-l", RemoteRevision: "rev-r",
	}
	if err := conflict.Write(filepath.Join(dir, ".rex", "specs", "x.yaml.conflict"), side); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	out, err := executeCommand(t, "sync", "resolve", "specs/x.yaml",
		"--workspace", dir, "--remote", "primary", "--url", "http://unused.invalid",
	)
	if err != nil {
		t.Fatalf("resolve: %v\n%s", err, out)
	}
	exists, err := conflict.Exists(filepath.Join(dir, ".rex", "specs", "x.yaml"))
	if err != nil || exists {
		t.Fatalf("sidecar should be gone; err=%v exists=%v", err, exists)
	}
}

func TestSyncResolveRefusesFileWithMarkers(t *testing.T) {
	t.Parallel()

	dir := initSyncWorkspace(t)
	body := "spec_version: 1\n<<<<<<< local\nx\n=======\ny\n>>>>>>> remote\n"
	writeRexFile(t, dir, "specs/x.yaml", body)
	side := conflict.Sidecar{Entity: "specs/x.yaml", Remote: "primary"}
	if err := conflict.Write(filepath.Join(dir, ".rex", "specs", "x.yaml.conflict"), side); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	_, err := executeCommand(t, "sync", "resolve", "specs/x.yaml",
		"--workspace", dir, "--remote", "primary", "--url", "http://unused.invalid",
	)
	if err == nil {
		t.Fatal("resolve should refuse a file with markers")
	}
	if !strings.Contains(err.Error(), "merge markers") {
		t.Fatalf("error should mention markers: %v", err)
	}
}

// TestSpecValidateRefusesConflictedSpec covers sync.GIT.3's
// "validate refuses conflicted entities" rule.
func TestSpecValidateRefusesConflictedSpec(t *testing.T) {
	t.Parallel()

	dir := initSyncWorkspace(t)
	specPath := filepath.Join(dir, ".rex", "specs", "demo.yaml")
	body := []byte(`spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
description: |
  d
`)
	if err := os.WriteFile(specPath, body, 0o644); err != nil {
		t.Fatalf("seed spec: %v", err)
	}
	// Pre-stamp a sidecar to flag the spec as conflicted.
	side := conflict.Sidecar{Entity: "specs/demo.yaml", Remote: "primary"}
	if err := conflict.Write(specPath+".conflict", side); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	out, err := executeCommand(t, "spec", "validate", "demo", "--workspace", dir)
	if err == nil {
		t.Fatalf("validate should refuse conflicted spec; out:\n%s", out)
	}
}

// TestSyncRebaseJSONOutput covers --json output for the rebase
// surface so external tools can consume the result.
func TestSyncRebaseJSONOutput(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)
	writeRexFile(t, dir, "specs/y.yaml", "spec_version: 1\n")

	out, err := executeCommand(t, "sync", "rebase", "specs/y.yaml",
		"--workspace", dir, "--url", hs.URL, "--json",
	)
	if err != nil {
		t.Fatalf("rebase: %v\n%s", err, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if v["entity"] != "specs/y.yaml" {
		t.Fatalf("entity: got %v", v["entity"])
	}
	if v["outcome"] != "local-only" {
		t.Fatalf("outcome: got %v want local-only", v["outcome"])
	}
}
