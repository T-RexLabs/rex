package specverify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/specfmt"
)

// makeWorkspaceWithSpec wires a one-spec workspace whose only
// task carries the supplied proof entries. Returns the workspace
// + the on-disk root (so tests can drop fixture files alongside).
func makeWorkspaceWithSpec(t *testing.T, taskState string, entries []specfmt.ProofEntry) (*specfmt.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	doc := &specfmt.Document{
		SpecVersion: 1,
		Metadata: specfmt.Metadata{
			ID:    "verifier-test",
			Name:  "Verifier test",
			State: "draft",
		},
		Tasks: []specfmt.Task{
			{
				ID:          "the-task",
				Description: "Exercises verify",
				State:       taskState,
				Proof:       specfmt.Proof{Entries: entries},
			},
		},
		Path: filepath.Join(root, "specs", "verifier-test.yaml"),
	}
	ws := specfmt.NewWorkspace()
	if err := ws.Add(doc); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return ws, root
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestVerifyCodePathExists is the happy path for kind: code:
// path resolves on disk → no issues.
func TestVerifyCodePathExists(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCode, Path: "internal/foo.go"},
	})
	writeFile(t, root, "internal/foo.go", "package foo\n")

	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Issues) != 0 {
		t.Fatalf("expected no issues, got %v", res.Issues)
	}
}

func TestVerifyCodePathMissingStrictError(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCode, Path: "internal/missing.go"},
	})
	res := Verify(ws, Options{WorkspaceRoot: root})
	if !res.HasErrors() {
		t.Fatalf("expected strict error; got %v", res.Issues)
	}
	if !strings.Contains(res.Errors()[0].Message, "does not exist") {
		t.Fatalf("wrong message: %v", res.Errors()[0].Message)
	}
}

func TestVerifyCodePathMissingLenientWarning(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCode, Path: "internal/missing.go"},
	})
	res := Verify(ws, Options{WorkspaceRoot: root, Mode: specfmt.ModeLenient})
	if res.HasErrors() {
		t.Fatalf("lenient mode should warn, not error: %v", res.Issues)
	}
	if len(res.Warnings()) != 1 {
		t.Fatalf("expected 1 warning; got %v", res.Issues)
	}
}

// TestVerifyCodePathMissingButIgnoredIsWarning covers the
// parked/external-tree case: a missing path that git reports as
// ignored is absent-by-design (e.g. a module being broken out), so
// even in strict mode it downgrades to a warning rather than failing
// a partial checkout — mirroring the run-id clone-tolerance.
func TestVerifyCodePathMissingButIgnoredIsWarning(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCode, Path: "rex-centralized/central/server/server.go"},
	})
	res := Verify(ws, Options{
		WorkspaceRoot: root,
		pathIgnored:   func(string) bool { return true },
	})
	if res.HasErrors() {
		t.Fatalf("git-ignored missing path should warn, not error: %v", res.Issues)
	}
	if len(res.Warnings()) != 1 {
		t.Fatalf("expected 1 parked-path warning; got %v", res.Issues)
	}
	if res.Warnings()[0].Category != "parked-path" {
		t.Fatalf("wrong category: %v", res.Warnings()[0])
	}
}

// TestVerifyCodePathMissingNotIgnoredStaysError confirms the
// downgrade is gated on the ignore check: a missing path that is NOT
// git-ignored is still a strict error, so genuine path drift on
// tracked code keeps failing.
func TestVerifyCodePathMissingNotIgnoredStaysError(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCode, Path: "internal/missing.go"},
	})
	res := Verify(ws, Options{
		WorkspaceRoot: root,
		pathIgnored:   func(string) bool { return false },
	})
	if !res.HasErrors() {
		t.Fatalf("non-ignored missing path must stay a strict error; got %v", res.Issues)
	}
}

// TestVerifyTestFuncFound greps for the test function name and
// passes when present.
func TestVerifyTestFuncFound(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindTest, Path: "foo_test.go", Name: "TestThing"},
	})
	writeFile(t, root, "foo_test.go", `package foo
import "testing"
func TestThing(t *testing.T) {}
`)
	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Issues) != 0 {
		t.Fatalf("expected clean; got %v", res.Issues)
	}
}

// TestVerifyTestFuncFoundWithMethodReceiver tolerates methods on
// receivers (table-driven tests sometimes wrap helper methods).
func TestVerifyTestFuncFoundWithMethodReceiver(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindTest, Path: "foo_test.go", Name: "TestThing"},
	})
	writeFile(t, root, "foo_test.go", `package foo
import "testing"
type tester struct{}
func (s *tester) TestThing(t *testing.T) {}
`)
	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Issues) != 0 {
		t.Fatalf("expected clean; got %v", res.Issues)
	}
}

func TestVerifyTestFuncMissing(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindTest, Path: "foo_test.go", Name: "TestNope"},
	})
	writeFile(t, root, "foo_test.go", "package foo\n")
	res := Verify(ws, Options{WorkspaceRoot: root})
	if !res.HasErrors() {
		t.Fatalf("expected strict error; got %v", res.Issues)
	}
	if !strings.Contains(res.Errors()[0].Message, "TestNope") {
		t.Fatalf("wrong message: %v", res.Errors()[0].Message)
	}
}

// TestVerifyRunIDFound uses the injected lookup hook so we don't
// need a real events.log.
func TestVerifyRunIDFound(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindRun, RunID: "r-1"},
	})
	res := Verify(ws, Options{
		WorkspaceRoot: root,
		runIDLookup: func(id string) (bool, error) {
			return id == "r-1", nil
		},
	})
	if len(res.Issues) != 0 {
		t.Fatalf("expected clean; got %v", res.Issues)
	}
}

func TestVerifyRunIDMissingIsWarning(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindRun, RunID: "r-missing"},
	})
	// Even in strict mode, missing-run is a warning per
	// PROOF.3 — fresh clones may not have synced the run yet.
	res := Verify(ws, Options{
		WorkspaceRoot: root,
		runIDLookup:   func(string) (bool, error) { return false, nil },
	})
	if res.HasErrors() {
		t.Fatalf("expected warning, not error: %v", res.Issues)
	}
	if len(res.Warnings()) != 1 {
		t.Fatalf("want 1 warning, got %v", res.Issues)
	}
}

// TestVerifyCommitRefReachable injects a stub git lookup to keep
// the test independent of the host's git/state.
func TestVerifyCommitRefReachable(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCommit, Ref: "abc1234"},
	})
	res := Verify(ws, Options{
		WorkspaceRoot: root,
		GitDir:        root,
		gitRefExists:  func(ref string) (bool, error) { return ref == "abc1234", nil },
	})
	if len(res.Issues) != 0 {
		t.Fatalf("expected clean; got %v", res.Issues)
	}
}

func TestVerifyCommitRefMissing(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCommit, Ref: "deadbeef"},
	})
	res := Verify(ws, Options{
		WorkspaceRoot: root,
		GitDir:        root,
		gitRefExists:  func(string) (bool, error) { return false, nil },
	})
	if !res.HasErrors() {
		t.Fatalf("expected strict error; got %v", res.Issues)
	}
}

// TestVerifyCommitRefSkippedWithoutGitDir warns rather than
// erroring when no git context is available (e.g. running in a
// fresh clone that hasn't been git-init'd).
func TestVerifyCommitRefSkippedWithoutGitDir(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindCommit, Ref: "abc"},
	})
	res := Verify(ws, Options{WorkspaceRoot: root})
	if res.HasErrors() {
		t.Fatalf("no-git should be a warning: %v", res.Issues)
	}
	if len(res.Warnings()) != 1 {
		t.Fatalf("want 1 warning, got %v", res.Issues)
	}
}

// TestVerifySpecACIDResolves uses a second spec in the workspace
// so the cited ACID actually has a target.
func TestVerifySpecACIDResolves(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindSpec, ACID: "other.X.1"},
	})
	other := &specfmt.Document{
		SpecVersion: 1,
		Metadata:    specfmt.Metadata{ID: "other", Name: "Other", State: "draft"},
		Components: map[string]specfmt.Component{
			"X": {Name: "X", Requirements: map[string]specfmt.Requirement{
				"1": {Text: "first"},
			}},
		},
	}
	if err := ws.Add(other); err != nil {
		t.Fatalf("Add: %v", err)
	}
	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Issues) != 0 {
		t.Fatalf("expected clean; got %v", res.Issues)
	}
}

func TestVerifySpecACIDDangling(t *testing.T) {
	t.Parallel()
	ws, root := makeWorkspaceWithSpec(t, "done", []specfmt.ProofEntry{
		{Kind: specfmt.ProofKindSpec, ACID: "ghost.GONE.99"},
	})
	res := Verify(ws, Options{WorkspaceRoot: root})
	if !res.HasErrors() {
		t.Fatalf("expected dangling-acid error; got %v", res.Issues)
	}
	if !strings.Contains(res.Errors()[0].Category, "dangling-acid") {
		t.Fatalf("wrong category: %v", res.Errors()[0])
	}
}

// TestVerifyOnlyExercisesStructuredProof confirms tasks with a
// free-form string proof (lenient-mode artifact) are skipped
// silently — there's nothing to verify.
func TestVerifyOnlyExercisesStructuredProof(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doc := &specfmt.Document{
		SpecVersion: 1,
		Metadata:    specfmt.Metadata{ID: "verifier-test", Name: "X", State: "draft"},
		Tasks: []specfmt.Task{
			{ID: "t1", Description: "...", State: "in_progress",
				Proof: specfmt.Proof{Text: "scratchpad note, no entries"}},
		},
	}
	ws := specfmt.NewWorkspace()
	_ = ws.Add(doc)
	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Issues) != 0 {
		t.Fatalf("expected silence; got %v", res.Issues)
	}
}

func TestVerifyRequiresWorkspaceRoot(t *testing.T) {
	t.Parallel()
	ws := specfmt.NewWorkspace()
	res := Verify(ws, Options{})
	if !res.HasErrors() {
		t.Fatalf("expected workspace-root error")
	}
	if !strings.Contains(res.Errors()[0].Message, "WorkspaceRoot") {
		t.Fatalf("wrong message: %v", res.Errors()[0])
	}
}

// TestVerifyEventsLogScannerHandlesMissingFile drives
// makeRunIDLookup directly with a path that doesn't exist —
// should report (false, nil), not fail.
func TestVerifyEventsLogScannerHandlesMissingFile(t *testing.T) {
	t.Parallel()
	lookup := makeRunIDLookup(filepath.Join(t.TempDir(), "no-such-events.log"))
	ok, err := lookup("anything")
	if err != nil {
		t.Fatalf("missing-file should be (false, nil); got err=%v", err)
	}
	if ok {
		t.Fatalf("expected not-found; got found")
	}
}

// TestVerifySkippedWhenNoEntries confirms the trivial case
// (workspace with one task, no proof) is silent — there's
// nothing for verify to exercise.
func TestVerifySkippedWhenNoEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doc := &specfmt.Document{
		SpecVersion: 1,
		Metadata:    specfmt.Metadata{ID: "x", Name: "X", State: "draft"},
		Tasks: []specfmt.Task{
			{ID: "t", Description: "d", State: "todo"},
		},
	}
	ws := specfmt.NewWorkspace()
	_ = ws.Add(doc)
	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Issues) != 0 {
		t.Fatalf("expected clean; got %v", res.Issues)
	}
}

// TestVerifyAggregatesAcrossTasksAndSpecs wires two specs each
// with multiple bad entries to confirm we don't short-circuit on
// the first failure — a real spec verify should report
// everything that's wrong, not stop at the first.
func TestVerifyAggregatesAcrossTasksAndSpecs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	docA := &specfmt.Document{
		SpecVersion: 1,
		Metadata:    specfmt.Metadata{ID: "a", Name: "A", State: "draft"},
		Tasks: []specfmt.Task{
			{ID: "t1", Description: "d", State: "done", Proof: specfmt.Proof{Entries: []specfmt.ProofEntry{
				{Kind: specfmt.ProofKindCode, Path: "missing-1.go"},
				{Kind: specfmt.ProofKindCode, Path: "missing-2.go"},
			}}},
		},
	}
	docB := &specfmt.Document{
		SpecVersion: 1,
		Metadata:    specfmt.Metadata{ID: "b", Name: "B", State: "draft"},
		Tasks: []specfmt.Task{
			{ID: "t1", Description: "d", State: "done", Proof: specfmt.Proof{Entries: []specfmt.ProofEntry{
				{Kind: specfmt.ProofKindSpec, ACID: "ghost.GONE.99"},
			}}},
		},
	}
	ws := specfmt.NewWorkspace()
	if err := errors.Join(ws.Add(docA), ws.Add(docB)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	res := Verify(ws, Options{WorkspaceRoot: root})
	if len(res.Errors()) != 3 {
		t.Fatalf("expected 3 errors (2 missing paths + 1 dangling acid); got %d: %v",
			len(res.Errors()), res.Issues)
	}
}
