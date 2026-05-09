package merge3

import (
	"strings"
	"testing"
)

func TestMergeBothSidesUnchanged(t *testing.T) {
	t.Parallel()

	r := Merge([]byte("alpha\n"), []byte("alpha\n"), []byte("alpha\n"))
	if !r.Clean() {
		t.Fatalf("Clean: %v", r.Conflicts)
	}
	if string(r.Merged) != "alpha\n" {
		t.Fatalf("merged: %q", r.Merged)
	}
}

func TestMergeOnlyLocalChanged(t *testing.T) {
	t.Parallel()

	r := Merge([]byte("alpha\n"), []byte("beta\n"), []byte("alpha\n"))
	if !r.Clean() {
		t.Fatalf("expected clean merge: %+v", r.Conflicts)
	}
	if string(r.Merged) != "beta\n" {
		t.Fatalf("merged: %q", r.Merged)
	}
}

func TestMergeOnlyRemoteChanged(t *testing.T) {
	t.Parallel()

	r := Merge([]byte("alpha\n"), []byte("alpha\n"), []byte("gamma\n"))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v", r.Conflicts)
	}
	if string(r.Merged) != "gamma\n" {
		t.Fatalf("merged: %q", r.Merged)
	}
}

func TestMergeBothSidesIdenticalEdit(t *testing.T) {
	t.Parallel()

	r := Merge([]byte("alpha\n"), []byte("zeta\n"), []byte("zeta\n"))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v", r.Conflicts)
	}
	if string(r.Merged) != "zeta\n" {
		t.Fatalf("merged: %q", r.Merged)
	}
}

func TestMergeNonOverlappingChanges(t *testing.T) {
	t.Parallel()

	base := "line1\nline2\nline3\n"
	local := "LINE1\nline2\nline3\n"
	remote := "line1\nline2\nLINE3\n"
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if !r.Clean() {
		t.Fatalf("expected clean (non-overlapping changes): %+v", r.Conflicts)
	}
	want := "LINE1\nline2\nLINE3\n"
	if string(r.Merged) != want {
		t.Fatalf("merged:\n%s\nwant:\n%s", r.Merged, want)
	}
}

func TestMergeOverlappingChangeIsConflict(t *testing.T) {
	t.Parallel()

	r := Merge([]byte("alpha\n"), []byte("beta\n"), []byte("gamma\n"))
	if r.Clean() {
		t.Fatalf("expected conflict, got clean merge:\n%s", r.Merged)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("conflicts: got %d want 1", len(r.Conflicts))
	}
	got := string(r.Merged)
	for _, want := range []string{MarkerLocal, MarkerSeparator, MarkerRemote, "beta", "gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged missing %q:\n%s", want, got)
		}
	}
}

// TestMergeLocalAddsLineRemoteUnchanged covers an additive change
// past the end of base. Common in spec tasks where local appends a
// new requirement.
func TestMergeLocalAddsLineRemoteUnchanged(t *testing.T) {
	t.Parallel()

	base := "alpha\nbeta\n"
	local := "alpha\nbeta\nGAMMA\n"
	remote := "alpha\nbeta\n"
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v", r.Conflicts)
	}
	if string(r.Merged) != local {
		t.Fatalf("merged:\n%s\nwant:\n%s", r.Merged, local)
	}
}

// TestMergeBothAddDifferentTrailingLines: both sides append new
// distinct lines past the end of base. Standard diff3 reports a
// conflict here because both edited the same "end-of-file" region.
func TestMergeBothAddDifferentTrailingLines(t *testing.T) {
	t.Parallel()

	base := "alpha\n"
	local := "alpha\nLOCAL\n"
	remote := "alpha\nREMOTE\n"
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if r.Clean() {
		t.Fatalf("expected conflict at trailing additions: %s", r.Merged)
	}
}

// TestMergeRemoteDeletesLineLocalUnchanged: clean delete on the
// remote side, no edits on the local side → take remote.
func TestMergeRemoteDeletesLineLocalUnchanged(t *testing.T) {
	t.Parallel()

	base := "alpha\nbeta\ngamma\n"
	local := "alpha\nbeta\ngamma\n"
	remote := "alpha\ngamma\n"
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v", r.Conflicts)
	}
	if string(r.Merged) != remote {
		t.Fatalf("merged:\n%s\nwant:\n%s", r.Merged, remote)
	}
}

// TestMergeBothEditDifferentRegions: local edits line 1, remote
// edits line 5. Should be clean.
func TestMergeBothEditDifferentRegions(t *testing.T) {
	t.Parallel()

	base := "a\nb\nc\nd\ne\n"
	local := "A\nb\nc\nd\ne\n"
	remote := "a\nb\nc\nd\nE\n"
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v\n%s", r.Conflicts, r.Merged)
	}
	want := "A\nb\nc\nd\nE\n"
	if string(r.Merged) != want {
		t.Fatalf("merged:\n%s\nwant:\n%s", r.Merged, want)
	}
}

// TestMergeConflictHunkLineNumbers confirms the ConflictHunk fields
// pin down the conflicted region precisely so the sidecar can be
// rendered with usable coordinates.
func TestMergeConflictHunkLineNumbers(t *testing.T) {
	t.Parallel()

	base := "h1\nh2\nMID\nh3\nh4\n"
	local := "h1\nh2\nLOCAL\nh3\nh4\n"
	remote := "h1\nh2\nREMOTE\nh3\nh4\n"
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if r.Clean() {
		t.Fatalf("expected conflict: %s", r.Merged)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("conflicts: got %d want 1", len(r.Conflicts))
	}
	c := r.Conflicts[0]
	if c.BaseStart != 3 || c.BaseEnd != 4 {
		t.Errorf("base range: %d..%d want 3..4", c.BaseStart, c.BaseEnd)
	}
	if c.LocalStart != 3 || c.LocalEnd != 4 {
		t.Errorf("local range: %d..%d want 3..4", c.LocalStart, c.LocalEnd)
	}
	if c.RemoteStart != 3 || c.RemoteEnd != 4 {
		t.Errorf("remote range: %d..%d want 3..4", c.RemoteStart, c.RemoteEnd)
	}
	if len(c.LocalLines) != 1 || c.LocalLines[0] != "LOCAL" {
		t.Errorf("local lines: %v", c.LocalLines)
	}
}

// TestMergeYAMLPayload mimics a representative spec edit:
// metadata.updated_at bumped on local, a single description line
// rewritten on remote. The fields are far apart so the merge is clean.
func TestMergeYAMLPayload(t *testing.T) {
	t.Parallel()

	base := `metadata:
  id: x
  updated_at: 2026-05-08T00:00:00Z

description: |
  Short body.

tasks:
  - id: alpha
    state: todo
`
	local := `metadata:
  id: x
  updated_at: 2026-05-09T00:00:00Z

description: |
  Short body.

tasks:
  - id: alpha
    state: todo
`
	remote := `metadata:
  id: x
  updated_at: 2026-05-08T00:00:00Z

description: |
  Updated body.

tasks:
  - id: alpha
    state: todo
`
	r := Merge([]byte(base), []byte(local), []byte(remote))
	if !r.Clean() {
		t.Fatalf("expected clean YAML merge: %+v\n%s", r.Conflicts, r.Merged)
	}
	if !strings.Contains(string(r.Merged), "2026-05-09T00:00:00Z") ||
		!strings.Contains(string(r.Merged), "Updated body.") {
		t.Fatalf("merged dropped one side's edit:\n%s", r.Merged)
	}
}

func TestMergeTrailingNewlinePreserved(t *testing.T) {
	t.Parallel()

	// All inputs end with a newline → output should too.
	r := Merge([]byte("a\nb\n"), []byte("a\nB\n"), []byte("A\nb\n"))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v", r.Conflicts)
	}
	if !strings.HasSuffix(string(r.Merged), "\n") {
		t.Fatalf("trailing newline lost: %q", r.Merged)
	}

	// No input ends with a newline → output should not either.
	r2 := Merge([]byte("a\nb"), []byte("a\nB"), []byte("A\nb"))
	if !r2.Clean() {
		t.Fatalf("expected clean: %+v", r2.Conflicts)
	}
	if strings.HasSuffix(string(r2.Merged), "\n") {
		t.Fatalf("unexpected trailing newline: %q", r2.Merged)
	}
}

// TestMergeEmptyBase covers the "first-time create on both sides"
// case where there is no common ancestor on disk yet.
func TestMergeEmptyBase(t *testing.T) {
	t.Parallel()

	// Identical creates → clean.
	r := Merge(nil, []byte("hello\n"), []byte("hello\n"))
	if !r.Clean() {
		t.Fatalf("expected clean: %+v", r.Conflicts)
	}

	// Divergent creates → conflict.
	r2 := Merge(nil, []byte("local\n"), []byte("remote\n"))
	if r2.Clean() {
		t.Fatalf("expected conflict on divergent first-creates: %s", r2.Merged)
	}
}
