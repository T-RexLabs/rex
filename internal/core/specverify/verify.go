// Package specverify exercises every structured proof entry in
// a workspace's specs against on-disk evidence. It is the
// load-bearing half of the "validate by code, not by harness"
// commitment: where specfmt.Validate enforces schema shape,
// specverify reaches out and confirms the citation actually
// resolves — file exists, test function present, run id in the
// events.log, commit reachable, ACID resolves.
//
// Lives in its own package because it imports the eventlog and
// runner packages (to look up runs) which would create a cycle
// if specfmt did the work directly. specfmt stays pure schema.
package specverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// Options configure Verify. WorkspaceRoot is the only required
// field; everything else is auto-derived or skipped when zero.
type Options struct {
	// WorkspaceRoot is the absolute workspace path used to
	// resolve `kind: code` and `kind: test` paths.
	WorkspaceRoot string
	// EventLogPath is the absolute path to the workspace's
	// events.log. When empty, run-id verification is skipped
	// with a "events.log not found locally" warning rather
	// than a hard error (mirroring spec-format.PROOF.3).
	EventLogPath string
	// GitDir is the directory to run git commands in (typically
	// equal to WorkspaceRoot or its parent if the workspace is a
	// subdirectory of the repo). When empty, commit-ref checks
	// are skipped with a warning.
	GitDir string
	// Mode is strict (default) or lenient.
	Mode specfmt.Mode
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// runID overrides via events.log scan; tests inject this
	// rather than seeding a real log.
	runIDLookup func(runID string) (bool, error)
	// gitRefExists overrides exec.Command-based git checks; tests
	// inject this so they don't depend on the host having git.
	gitRefExists func(ref string) (bool, error)
	// pathIgnored reports whether a workspace-relative path is
	// intentionally git-ignored. A missing path that is ignored is
	// a parked/external tree not present in this checkout (e.g. a
	// module being broken out), so its absence is a warning rather
	// than a hard error — mirroring the run-id clone-tolerance in
	// PROOF.3. Tests inject this; the default shells to
	// `git check-ignore` in GitDir.
	pathIgnored func(rel string) bool
}

// Verify walks every structured proof entry across every task in
// every spec the workspace registers. The returned Result mirrors
// specfmt.Validate's shape so callers can render both side by
// side via the same printer.
//
// Verify never modifies the workspace. The on-disk evidence is
// the source of truth; this function just queries it.
func Verify(ws *specfmt.Workspace, opts Options) specfmt.Result {
	if opts.WorkspaceRoot == "" {
		return specfmt.Result{Issues: []specfmt.Issue{{
			Path:     "",
			Category: "internal",
			Message:  "specverify: WorkspaceRoot is required",
			Severity: specfmt.SeverityError,
		}}}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.runIDLookup == nil {
		opts.runIDLookup = makeRunIDLookup(opts.EventLogPath)
	}
	if opts.gitRefExists == nil {
		opts.gitRefExists = makeGitRefLookup(opts.GitDir)
	}
	if opts.pathIgnored == nil {
		opts.pathIgnored = makePathIgnoredLookup(opts.GitDir)
	}

	v := &verifier{
		ws:   ws,
		opts: opts,
	}
	for _, doc := range ws.Specs() {
		for ti, task := range doc.Tasks {
			if !task.Proof.IsStructured() {
				continue
			}
			for ei, entry := range task.Proof.Entries {
				v.checkEntry(doc, ti, ei, entry)
			}
		}
	}
	return specfmt.Result{Issues: v.issues}
}

type verifier struct {
	ws     *specfmt.Workspace
	opts   Options
	issues []specfmt.Issue
}

func (v *verifier) issuePath(doc *specfmt.Document, taskIdx, entryIdx int) string {
	return fmt.Sprintf("tasks[%d].proof[%d]", taskIdx, entryIdx)
}

func (v *verifier) emit(doc *specfmt.Document, path, category, msg string, severity specfmt.Severity) {
	v.issues = append(v.issues, specfmt.Issue{
		File:     doc.Path,
		Path:     path,
		Category: category,
		Message:  msg,
		Severity: severity,
	})
}

func (v *verifier) strictOrLenient() specfmt.Severity {
	if v.opts.Mode == specfmt.ModeLenient {
		return specfmt.SeverityWarning
	}
	return specfmt.SeverityError
}

func (v *verifier) checkEntry(doc *specfmt.Document, taskIdx, entryIdx int, e specfmt.ProofEntry) {
	path := v.issuePath(doc, taskIdx, entryIdx)
	switch e.Kind {
	case specfmt.ProofKindCode:
		v.checkPathExists(doc, path, e.Path)
	case specfmt.ProofKindTest:
		if !v.checkPathExists(doc, path, e.Path) {
			return
		}
		if e.Name != "" {
			v.checkTestFunc(doc, path, e.Path, e.Name)
		}
	case specfmt.ProofKindRun:
		v.checkRunID(doc, path, e.RunID)
	case specfmt.ProofKindCommit:
		v.checkCommitRef(doc, path, e.Ref)
	case specfmt.ProofKindSpec:
		v.checkSpecACID(doc, path, e.ACID)
	}
}

// checkPathExists confirms a workspace-relative path resolves on
// disk. Reports a strict-or-lenient issue when missing. Returns
// true on success so callers can chain follow-up checks.
func (v *verifier) checkPathExists(doc *specfmt.Document, path, rel string) bool {
	if rel == "" {
		// Already caught by specfmt.Validate; nothing to do.
		return false
	}
	abs := filepath.Join(v.opts.WorkspaceRoot, rel)
	info, err := os.Stat(abs)
	if err == nil {
		_ = info // existence is enough; we accept files OR directories
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		// A missing path that is intentionally git-ignored is a
		// parked/external tree not present in this checkout (e.g. a
		// module being broken out). Treat it like a not-yet-synced
		// run (PROOF.3): a warning in both modes, not a hard error,
		// so partial checkouts (CI, fresh clones) still pass while
		// genuine path drift on tracked code stays a strict error.
		if v.opts.pathIgnored(rel) {
			v.emit(doc, path+".path", "parked-path",
				fmt.Sprintf("path %q is git-ignored and absent from this checkout — parked/external, not verified here (spec-format.PROOF.2)", rel),
				specfmt.SeverityWarning,
			)
			return false
		}
		v.emit(doc, path+".path", "missing-path",
			fmt.Sprintf("path %q does not exist on disk (spec-format.PROOF.2)", rel),
			v.strictOrLenient(),
		)
		return false
	}
	v.emit(doc, path+".path", "stat-error",
		fmt.Sprintf("stat %q: %v", rel, err),
		v.strictOrLenient(),
	)
	return false
}

// checkTestFunc greps the file for `func <name>(`. Misses are
// strict error / lenient warning per spec-format.PROOF.2.1.
func (v *verifier) checkTestFunc(doc *specfmt.Document, path, rel, name string) {
	abs := filepath.Join(v.opts.WorkspaceRoot, rel)
	body, err := os.ReadFile(abs)
	if err != nil {
		v.emit(doc, path+".name", "stat-error",
			fmt.Sprintf("read %q: %v", rel, err),
			v.strictOrLenient(),
		)
		return
	}
	// Match `func TestX(` or `func (...) TestX(` (methods on a
	// receiver) so subtest helpers + table-driven test files keep
	// working. The right-paren bound stops greedy matches.
	pattern := regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s+)?` + regexp.QuoteMeta(name) + `\s*\(`)
	if pattern.Match(body) {
		return
	}
	v.emit(doc, path+".name", "missing-test-func",
		fmt.Sprintf("test function %q not found in %q (spec-format.PROOF.2.1)", name, rel),
		v.strictOrLenient(),
	)
}

// checkRunID looks the run up in the workspace's events.log.
// Misses are warnings in both modes per spec-format.PROOF.3 — a
// fresh clone may not have synced the run yet.
func (v *verifier) checkRunID(doc *specfmt.Document, path, runID string) {
	if runID == "" {
		return
	}
	found, err := v.opts.runIDLookup(runID)
	if err != nil {
		v.emit(doc, path+".run_id", "events-log",
			fmt.Sprintf("scan events.log for run %q: %v", runID, err),
			specfmt.SeverityWarning,
		)
		return
	}
	if !found {
		v.emit(doc, path+".run_id", "missing-run",
			fmt.Sprintf("run %q not found in workspace events.log (spec-format.PROOF.3 — may not have synced yet)", runID),
			specfmt.SeverityWarning,
		)
	}
}

// checkCommitRef shells out to `git cat-file -e <ref>^{commit}`.
// Misses are strict error / lenient warning per
// spec-format.PROOF.4. Skipped (warning) when GitDir is empty.
func (v *verifier) checkCommitRef(doc *specfmt.Document, path, ref string) {
	if ref == "" {
		return
	}
	if v.opts.GitDir == "" {
		v.emit(doc, path+".ref", "git-unavailable",
			fmt.Sprintf("commit %q not verified — no GitDir configured", ref),
			specfmt.SeverityWarning,
		)
		return
	}
	ok, err := v.opts.gitRefExists(ref)
	if err != nil {
		v.emit(doc, path+".ref", "git-error",
			fmt.Sprintf("git cat-file %q: %v", ref, err),
			v.strictOrLenient(),
		)
		return
	}
	if !ok {
		v.emit(doc, path+".ref", "missing-commit",
			fmt.Sprintf("commit %q not reachable from %s (spec-format.PROOF.4)", ref, v.opts.GitDir),
			v.strictOrLenient(),
		)
	}
}

// checkSpecACID resolves the cited reference through the existing
// ACID resolver. Dangling refs are strict error / lenient warning
// per spec-format.PROOF.5.
func (v *verifier) checkSpecACID(doc *specfmt.Document, path, raw string) {
	if raw == "" {
		return
	}
	acid, err := specfmt.ParseACID(raw)
	if err != nil {
		v.emit(doc, path+".acid", "acid",
			fmt.Sprintf("acid %q: %v", raw, err),
			v.strictOrLenient(),
		)
		return
	}
	res := v.ws.Resolve(acid, doc.Metadata.ID)
	if !res.Found {
		v.emit(doc, path+".acid", "dangling-acid",
			fmt.Sprintf("acid %q resolves to %s but no such requirement exists (spec-format.PROOF.5)",
				raw, res.FullACID),
			v.strictOrLenient(),
		)
	}
}

// makeRunIDLookup returns a closure that scans the workspace's
// events.log for a run.started event matching runID. Empty
// logPath means the closure always reports "not found" without
// reading anything (the warning above covers it).
func makeRunIDLookup(logPath string) func(string) (bool, error) {
	if logPath == "" {
		return func(string) (bool, error) { return false, nil }
	}
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)
	return func(runID string) (bool, error) {
		r, err := eventlog.OpenReader(logPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// No log yet on this workspace — same as
				// "not found", treated as warning above.
				return false, nil
			}
			return false, err
		}
		defer r.Close()
		for {
			rec, err := r.Next()
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if rec.Type != runner.EventTypeRunStarted {
				continue
			}
			var probe struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rec.Payload, &probe); err != nil {
				continue
			}
			if probe.RunID == runID {
				return true, nil
			}
		}
	}
}

// makeGitRefLookup runs `git cat-file -e <ref>^{commit}` in
// gitDir. Returns (ok, err) where ok=true iff the ref resolves to
// a commit. Non-zero exit with no stderr error counts as "ref
// missing" (ok=false, err=nil) so we can differentiate that from
// a hard git failure.
func makeGitRefLookup(gitDir string) func(string) (bool, error) {
	if gitDir == "" {
		return func(string) (bool, error) { return false, nil }
	}
	return func(ref string) (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", ref+"^{commit}")
		cmd.Dir = gitDir
		// Discard stderr explicitly so a benign "not a valid
		// object name" doesn't leak to the user's TTY.
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				// Non-zero exit = ref doesn't resolve. Not a
				// tool failure.
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
}

// makePathIgnoredLookup runs `git check-ignore -q <rel>` in gitDir.
// Returns true iff git reports the path as ignored (exit 0). git
// check-ignore matches on the pathname rules, so it works for paths
// that don't exist on disk — exactly the parked-tree case. Empty
// gitDir (or any git failure) yields false, so absence stays a
// strict error unless git can positively confirm the ignore rule.
func makePathIgnoredLookup(gitDir string) func(string) bool {
	if gitDir == "" {
		return func(string) bool { return false }
	}
	return func(rel string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "check-ignore", "-q", "--", rel)
		cmd.Dir = gitDir
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		// Exit 0 = ignored; exit 1 = not ignored; >1 = git error.
		// Only a clean exit 0 counts as "parked".
		return cmd.Run() == nil
	}
}

// FromCLI is a convenience that builds Options from a workspace
// root by deriving EventLogPath and GitDir per project layout
// conventions. The CLI calls this; tests usually construct
// Options directly so they can inject the lookup hooks.
func FromCLI(workspaceRoot string, mode specfmt.Mode) Options {
	logPath := filepath.Join(workspaceRoot, ".rex", "events.log")
	if _, err := os.Stat(logPath); err != nil {
		logPath = ""
	}
	gitDir := workspaceRoot
	if !isGitDir(gitDir) {
		gitDir = ""
	}
	return Options{
		WorkspaceRoot: workspaceRoot,
		EventLogPath:  logPath,
		GitDir:        gitDir,
		Mode:          mode,
	}
}

// isGitDir reports whether running git commands in `dir` would
// resolve to a real git repository. We probe rather than just
// checking for `.git` so worktrees, submodules, and parent-tree
// repos (the common case where the workspace is a subdir of a
// monorepo) all work.
func isGitDir(dir string) bool {
	if dir == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// SortIssues orders issues for stable rendering: error before
// warning, then by file, then by path. Mirrors specfmt.SortIssues
// so the CLI can reuse the same printer for both surfaces.
func SortIssues(in []specfmt.Issue) []specfmt.Issue {
	out := make([]specfmt.Issue, len(in))
	copy(out, in)
	// reuse specfmt's helper so tooling stays consistent.
	return specfmt.SortIssues(out)
}
