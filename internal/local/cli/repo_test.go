package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// initWorkspace boots a workspace at dir and returns dir for chaining.
func initWorkspace(t *testing.T, dir string) string {
	t.Helper()
	if _, err := executeCommand(t, "init", dir); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

// readWorkspaceRepos pulls the `repos` slice straight off disk so
// the test asserts against bytes, not against the cached slice the
// test happens to have built.
func readWorkspaceRepos(t *testing.T, root string) []repoEntry {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	var doc struct {
		Repos []repoEntry `yaml:"repos"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("yaml parse: %v", err)
	}
	return doc.Repos
}

// readAuditEvents returns every event in events.log of the given
// types. Used to assert the repo.added / repo.linked / repo.removed
// audit trail after a command run.
func readAuditEvents(t *testing.T, root string, types ...string) []eventlog.Record {
	t.Helper()
	want := map[string]struct{}{}
	for _, ty := range types {
		want[ty] = struct{}{}
	}
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		t.Fatalf("open events.log: %v", err)
	}
	defer r.Close()
	var out []eventlog.Record
	for {
		rec, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("read events.log: %v", err)
		}
		if _, ok := want[rec.Type]; ok || len(want) == 0 {
			out = append(out, rec)
		}
	}
	return out
}

func TestRepoLinkRegistersExistingDirectory(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	repoDir := filepath.Join(root, "vendored", "bar")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	out, err := executeCommand(t, "repo", "link", "--workspace", root, filepath.Join("vendored", "bar"))
	if err != nil {
		t.Fatalf("repo link: %v\n%s", err, out)
	}
	if !strings.Contains(out, "linked repo \"bar\"") {
		t.Fatalf("output: %q", out)
	}
	repos := readWorkspaceRepos(t, root)
	if len(repos) != 1 {
		t.Fatalf("want 1 repo, got %d (%v)", len(repos), repos)
	}
	got := repos[0]
	if got.Name != "bar" || got.Path != "vendored/bar" || got.URL != "" {
		t.Fatalf("unexpected entry: %+v", got)
	}

	events := readAuditEvents(t, root, audit.EventTypeRepoLinked)
	if len(events) != 1 {
		t.Fatalf("want 1 repo.linked event, got %d", len(events))
	}
}

func TestRepoLinkRefusesPathOutsideWorkspace(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	out, err := executeCommand(t, "repo", "link", "--workspace", root, "../somewhere-else")
	if err == nil {
		t.Fatalf("expected error, got: %s", out)
	}
	if !strings.Contains(err.Error(), "escapes the workspace root") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestRepoLinkRefusesAbsoluteOutsideRoot(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	other := t.TempDir()
	_, err := executeCommand(t, "repo", "link", "--workspace", root, other)
	if err == nil {
		t.Fatal("expected error linking absolute path outside workspace")
	}
	if !strings.Contains(err.Error(), "escapes the workspace root") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestRepoLinkRefusesNonDirectory(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "afile"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := executeCommand(t, "repo", "link", "--workspace", root, "afile")
	if err == nil {
		t.Fatal("expected error linking a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestRepoLinkRejectsDuplicateName(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if _, err := executeCommand(t, "repo", "link", "--workspace", root, "a", "--name", "shared"); err != nil {
		t.Fatalf("first link: %v", err)
	}
	_, err := executeCommand(t, "repo", "link", "--workspace", root, "b", "--name", "shared")
	if err == nil {
		t.Fatal("expected duplicate-name rejection")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestRepoListAndRemove(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	repoDir := filepath.Join(root, "vendored", "baz")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := executeCommand(t, "repo", "link", "--workspace", root, "vendored/baz"); err != nil {
		t.Fatalf("repo link: %v", err)
	}

	// list (text)
	out, err := executeCommand(t, "repo", "list", "--workspace", root)
	if err != nil {
		t.Fatalf("repo list: %v", err)
	}
	if !strings.Contains(out, "baz") || !strings.Contains(out, "vendored/baz") || !strings.Contains(out, "linked") {
		t.Fatalf("repo list output: %q", out)
	}

	// list --json
	out, err = executeCommand(t, "repo", "list", "--workspace", root, "--json")
	if err != nil {
		t.Fatalf("repo list --json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["name"] != "baz" || rows[0]["path"] != "vendored/baz" {
		t.Fatalf("json shape: %v", rows)
	}
	if _, ok := rows[0]["url"]; ok {
		t.Fatalf("linked repo should have no url field: %v", rows[0])
	}

	// remove (no purge)
	out, err = executeCommand(t, "repo", "remove", "--workspace", root, "baz")
	if err != nil {
		t.Fatalf("repo remove: %v", err)
	}
	if !strings.Contains(out, "removed repo \"baz\"") {
		t.Fatalf("remove output: %q", out)
	}
	if _, err := os.Stat(repoDir); err != nil {
		t.Fatalf("working copy should be intact when --purge omitted: %v", err)
	}
	if got := readWorkspaceRepos(t, root); len(got) != 0 {
		t.Fatalf("repos slot should be empty, got %v", got)
	}

	events := readAuditEvents(t, root, audit.EventTypeRepoRemoved)
	if len(events) != 1 {
		t.Fatalf("want 1 repo.removed event, got %d", len(events))
	}
}

func TestRepoRemovePurgeDeletesWorkingCopy(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	repoDir := filepath.Join(root, "vendored", "qux")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := executeCommand(t, "repo", "link", "--workspace", root, "vendored/qux"); err != nil {
		t.Fatalf("repo link: %v", err)
	}

	if _, err := executeCommand(t, "repo", "remove", "--workspace", root, "qux", "--purge"); err != nil {
		t.Fatalf("repo remove --purge: %v", err)
	}
	if _, err := os.Stat(repoDir); !os.IsNotExist(err) {
		t.Fatalf("working copy should be deleted: %v", err)
	}

	events := readAuditEvents(t, root, audit.EventTypeRepoRemoved)
	if len(events) != 1 {
		t.Fatalf("want 1 repo.removed event, got %d", len(events))
	}
	var payload audit.RepoRemovedEvent
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Purged || payload.Name != "qux" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRepoRemoveErrorsOnUnknownName(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	_, err := executeCommand(t, "repo", "remove", "--workspace", root, "nope")
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
	if !strings.Contains(err.Error(), "no repo named") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestRepoLinkPreservesUnknownWorkspaceFields proves the yaml.Node
// round-trip in saveRepoEntries doesn't drop fields we haven't wired
// into workspaceSettings (e.g. user-set default_repo). This is the
// reason saveRepoEntries goes through yaml.Node rather than the
// struct-based round-trip used elsewhere.
func TestRepoLinkPreservesUnknownWorkspaceFields(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	wsPath := filepath.Join(root, ".rex", "workspace.yaml")
	body, err := os.ReadFile(wsPath)
	if err != nil {
		t.Fatalf("read workspace.yaml: %v", err)
	}
	body = append(body, []byte("default_repo: not-yet-wired\n")...)
	if err := os.WriteFile(wsPath, body, 0o644); err != nil {
		t.Fatalf("write workspace.yaml: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "stuff"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := executeCommand(t, "repo", "link", "--workspace", root, "stuff"); err != nil {
		t.Fatalf("repo link: %v", err)
	}

	body, err = os.ReadFile(wsPath)
	if err != nil {
		t.Fatalf("re-read workspace.yaml: %v", err)
	}
	if !strings.Contains(string(body), "default_repo: not-yet-wired") {
		t.Fatalf("yaml.Node round-trip dropped unknown field; file:\n%s", body)
	}
	if !strings.Contains(string(body), "name: stuff") {
		t.Fatalf("repos entry missing; file:\n%s", body)
	}
}

func TestRepoAddClonesThroughGit(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// Create a bare upstream repo; this is what `rex repo add` will
	// clone from. Using an on-disk bare avoids needing network.
	// --initial-branch=main pins the default-branch name so CI hosts
	// whose git still defaults to "master" don't end up pushing to
	// refs/heads/main while HEAD still points at refs/heads/master,
	// which makes `git clone` produce an empty checkout.
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", upstream).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	// Seed one commit so `git clone` produces a non-empty checkout.
	work := filepath.Join(t.TempDir(), "seed")
	if out, err := exec.Command("git", "init", "--initial-branch=main", work).CombinedOutput(); err != nil {
		t.Fatalf("git init seed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"-c", "user.email=t@t", "-c", "user.name=t", "-C", work, "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "-C", work, "commit", "-m", "init"},
		{"-C", work, "remote", "add", "origin", upstream},
		{"-C", work, "push", "origin", "HEAD:refs/heads/main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	root := initWorkspace(t, t.TempDir())
	out, err := executeCommand(t, "repo", "add", "--workspace", root, upstream, "vendored/up")
	if err != nil {
		t.Fatalf("repo add: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "vendored", "up", "README.md")); err != nil {
		t.Fatalf("clone produced no README: %v", err)
	}
	repos := readWorkspaceRepos(t, root)
	if len(repos) != 1 || repos[0].URL != upstream || repos[0].Path != "vendored/up" {
		t.Fatalf("unexpected entry: %+v", repos)
	}

	events := readAuditEvents(t, root, audit.EventTypeRepoAdded)
	if len(events) != 1 {
		t.Fatalf("want 1 repo.added event, got %d", len(events))
	}
	var payload audit.RepoAddedEvent
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.URL != upstream || payload.Path != "vendored/up" || payload.Name != "up" {
		t.Fatalf("payload: %+v", payload)
	}
}

func TestRepoAddRefusesIfDestinationExists(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := initWorkspace(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "occupied"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := executeCommand(t, "repo", "add", "--workspace", root, "https://example.invalid/foo.git", "occupied")
	if err == nil {
		t.Fatal("expected error for occupied destination")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestDeriveRepoBasenameFromURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://github.com/foo/bar.git":  "bar",
		"https://github.com/foo/bar":      "bar",
		"git@github.com:foo/bar.git":      "bar",
		"./local-clone":                   "local-clone",
		"":                                "",
		"https://github.com/foo/bar.git/": "bar",
	}
	for in, want := range cases {
		if got := deriveRepoBasenameFromURL(in); got != want {
			t.Errorf("deriveRepoBasenameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
