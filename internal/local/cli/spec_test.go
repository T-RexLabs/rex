package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the absolute path of the git repo, computed from the
// package's location at build time. Tests use it to point spec
// commands at the real specs/ corpus.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/local/cli -> 3 up
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// makeFakeWorkspace builds a temp .rex/specs/ tree from the supplied
// files so tests can drive workspace-relative commands without needing
// the real repo layout.
func makeFakeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	specsDir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(specsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range files {
		path := filepath.Join(specsDir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestSpecValidateOnRealRepo(t *testing.T) {
	t.Parallel()

	specsPath := filepath.Join(repoRoot(t), "specs")
	matches, err := filepath.Glob(filepath.Join(specsPath, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) < 14 {
		t.Fatalf("found %d real specs, want >= 14", len(matches))
	}

	args := append([]string{"spec", "validate"}, matches...)
	out, err := executeCommand(t, args...)
	if err != nil {
		t.Fatalf("validate real specs: err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "0 error(s)") {
		t.Fatalf("expected 0 errors, got: %s", out)
	}
}

func TestSpecValidateFailsOnBrokenSpec(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"broken.yaml": `
spec_version: 1
metadata:
  id: broken
  name: Broken
  state: bogus
`,
	})
	out, err := executeCommand(t, "spec", "validate", "--workspace", root)
	if err == nil {
		t.Fatalf("expected error on broken spec; out=%s", out)
	}
	if !strings.Contains(out, "metadata.state") {
		t.Fatalf("expected metadata.state error in output: %s", out)
	}
}

func TestSpecValidateLenientMode(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"x.yaml": `
spec_version: 1
metadata: {id: x, name: X, state: draft}
weird_extra: hello
`,
	})
	// Strict: error
	if _, err := executeCommand(t, "spec", "validate", "--workspace", root); err == nil {
		t.Fatal("strict mode should fail on unknown top-level key")
	}
	// Lenient: warn but exit 0
	out, err := executeCommand(t, "spec", "validate", "--lenient", "--workspace", root)
	if err != nil {
		t.Fatalf("lenient should pass; out=%s err=%v", out, err)
	}
	if !strings.Contains(out, "warning") {
		t.Fatalf("lenient should warn: %s", out)
	}
}

func TestSpecValidateJSONFlag(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"broken.yaml": `
spec_version: 1
metadata: {id: broken, name: B, state: oops}
`,
	})
	out, _ := executeCommand(t, "spec", "validate", "--json", "--workspace", root)
	for _, line := range splitNonEmpty(out) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("non-JSON line: %q (err=%v)", line, err)
		}
	}
}

func TestSpecListShowsRealSpecs(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "spec", "list", "--workspace", filepath.Join(repoRoot(t), ".rex"))
	// .rex/ doesn't exist at repo root yet, so listing should produce
	// "no specs found".
	if err != nil {
		t.Fatalf("list: %v out=%s", err, out)
	}
	if !strings.Contains(out, "no specs found") {
		t.Fatalf("expected 'no specs found' for non-init repo, got: %s", out)
	}
}

func TestSpecListAgainstFakeWorkspace(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"alpha.yaml": `
spec_version: 1
metadata: {id: alpha, name: Alpha, state: draft}
tasks:
  - id: t1
    description: do
    state: todo
`,
		"beta.yaml": `
spec_version: 1
metadata: {id: beta, name: Beta, state: active}
`,
	})

	out, err := executeCommand(t, "spec", "list", "--workspace", root)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("output missing specs: %s", out)
	}

	// State filter
	out, err = executeCommand(t, "spec", "list", "--state", "active", "--workspace", root)
	if err != nil {
		t.Fatalf("list --state: %v\n%s", err, out)
	}
	if strings.Contains(out, "alpha") {
		t.Fatalf("--state active should exclude alpha: %s", out)
	}
	if !strings.Contains(out, "beta") {
		t.Fatalf("--state active should include beta: %s", out)
	}
}

func TestSpecShowByPath(t *testing.T) {
	t.Parallel()

	specPath := filepath.Join(repoRoot(t), "specs", "spec-format.yaml")
	out, err := executeCommand(t, "spec", "show", specPath)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "spec-format") {
		t.Fatalf("show should print id: %s", out)
	}
	if !strings.Contains(out, "components:") {
		t.Fatalf("show should list components: %s", out)
	}
}

func TestSpecACIDFullFormResolves(t *testing.T) {
	t.Parallel()

	// Build a workspace pointing at the real repo specs.
	root := t.TempDir()
	rexDir := filepath.Join(root, ".rex", "specs")
	if err := os.MkdirAll(rexDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(repoRoot(t), "specs", "*.yaml"))
	for _, src := range matches {
		dst := filepath.Join(rexDir, filepath.Base(src))
		body, _ := os.ReadFile(src)
		_ = os.WriteFile(dst, body, 0o644)
	}

	out, err := executeCommand(t, "spec", "acid", "overview.SYS.1", "--workspace", root)
	if err != nil {
		t.Fatalf("acid: %v\n%s", err, out)
	}
	if !strings.Contains(out, "overview.SYS.1") {
		t.Fatalf("acid output: %s", out)
	}
	if !strings.Contains(out, "component") {
		t.Fatalf("acid kind should mention component: %s", out)
	}
}

func TestSpecACIDDangling(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"x.yaml": `
spec_version: 1
metadata: {id: x, name: X, state: draft}
components:
  AUTH:
    name: Auth
    requirements:
      "1": exists
`,
	})

	_, err := executeCommand(t, "spec", "acid", "x.AUTH.999", "--workspace", root)
	if err == nil {
		t.Fatal("dangling ACID should error")
	}
	if !strings.Contains(err.Error(), "dangling") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestSpecACIDShortFormResolves(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"only.yaml": `
spec_version: 1
metadata: {id: only, name: Only, state: draft}
components:
  AUTH:
    name: Auth
    requirements:
      "1": only-spec target
`,
	})

	out, err := executeCommand(t, "spec", "acid", "AUTH.1", "--workspace", root)
	if err != nil {
		t.Fatalf("short ACID: %v\n%s", err, out)
	}
	if !strings.Contains(out, "only-spec target") {
		t.Fatalf("output should include requirement text: %s", out)
	}
}

func TestSpecACIDShortFormAmbiguous(t *testing.T) {
	t.Parallel()

	root := makeFakeWorkspace(t, map[string]string{
		"a.yaml": `
spec_version: 1
metadata: {id: a, name: A, state: draft}
components:
  AUTH:
    name: Auth
    requirements:
      "1": A's auth
`,
		"b.yaml": `
spec_version: 1
metadata: {id: b, name: B, state: draft}
components:
  AUTH:
    name: Auth
    requirements:
      "1": B's auth
`,
	})

	_, err := executeCommand(t, "spec", "acid", "AUTH.1", "--workspace", root)
	if err == nil {
		t.Fatal("ambiguous short ACID should error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error: %v", err)
	}
}

func TestSpecACIDBadShape(t *testing.T) {
	t.Parallel()

	_, err := executeCommand(t, "spec", "acid", "lol")
	if err == nil {
		t.Fatal("bad ACID syntax should error")
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
