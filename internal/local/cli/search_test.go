package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initSearchWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "sx", "--name", "SX"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

func TestSearchEmptyWorkspaceReportsNoMatches(t *testing.T) {
	t.Parallel()

	dir := initSearchWorkspace(t)
	out, err := executeCommand(t, "search", "missing-token", "--workspace", dir)
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("expected 'no matches': %s", out)
	}
}

func TestSearchFindsWorkspaceCreatedEvent(t *testing.T) {
	t.Parallel()

	dir := initSearchWorkspace(t)
	out, err := executeCommand(t, "search", "workspace.created", "--workspace", dir)
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "workspace.created") {
		t.Fatalf("expected workspace.created in results: %s", out)
	}
}

func TestSearchHyphenatedTokenAutoQuoted(t *testing.T) {
	t.Parallel()

	dir := initSearchWorkspace(t)
	if _, err := executeCommand(t, "run", "start",
		"--workspace", dir,
		"--shell", "echo hello",
		"--run-id", "kebab-id-test",
	); err != nil {
		t.Fatalf("run start: %v", err)
	}
	out, err := executeCommand(t, "search", "kebab-id-test", "--workspace", dir)
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "kebab-id-test") {
		t.Fatalf("hyphenated query failed: %s", out)
	}
}

func TestSearchJSON(t *testing.T) {
	t.Parallel()

	dir := initSearchWorkspace(t)
	out, err := executeCommand(t, "search", "workspace.created",
		"--workspace", dir, "--json",
	)
	if err != nil {
		t.Fatalf("search --json: %v\n%s", err, out)
	}
	for _, line := range splitNonEmpty(out) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("non-JSON line: %q", line)
		}
		if v["entity_type"] == nil {
			t.Fatalf("missing entity_type: %v", v)
		}
	}
}

func TestSearchLimitFlag(t *testing.T) {
	t.Parallel()

	dir := initSearchWorkspace(t)
	// Generate enough events to exceed the limit.
	for i := 0; i < 4; i++ {
		_, _ = executeCommand(t, "run", "start",
			"--workspace", dir,
			"--shell", "echo limit-marker",
			"--run-id", "limit-"+itoaTest(i),
		)
	}
	out, err := executeCommand(t, "search", "limit-marker",
		"--workspace", dir,
		"--limit", "2",
		"--json",
	)
	if err != nil {
		t.Fatalf("search --limit: %v", err)
	}
	if got := len(splitNonEmpty(out)); got > 2 {
		t.Fatalf("limit not respected: got %d lines\n%s", got, out)
	}
}

func TestWorkspaceReindexRebuilds(t *testing.T) {
	t.Parallel()

	dir := initSearchWorkspace(t)
	// Add a spec post-init; it's not yet in the index because
	// indexing only happens on event append.
	specPath := filepath.Join(dir, ".rex", "specs", "alpha.yaml")
	if err := os.WriteFile(specPath, []byte(`spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
description: |
  searchable-after-reindex
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	// Search before reindex — spec should not appear.
	out, err := executeCommand(t, "search", "searchable-after-reindex",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("search before reindex: %v", err)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("expected no matches before reindex: %s", out)
	}

	// Reindex.
	out, err = executeCommand(t, "workspace", "reindex", "--workspace", dir)
	if err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	if !strings.Contains(out, "reindexed") {
		t.Fatalf("reindex output: %s", out)
	}

	// Search after reindex — spec is now in the index.
	out, err = executeCommand(t, "search", "searchable-after-reindex",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("search after reindex: %v", err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("expected alpha in results: %s", out)
	}
}

// itoaTest is a tiny helper avoiding strconv just for a single-digit conversion.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
