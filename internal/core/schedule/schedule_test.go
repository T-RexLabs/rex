package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/specfmt"
)

// writeYAML drops body at <dir>/<name>.yaml so loadtests can assert
// against real on-disk schedules.
func writeYAML(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLoadFileCronShellOK(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeYAML(t, dir, "nightly", `
spec_version: 1
name: nightly
trigger:
  kind: cron
  cron: "0 3 * * *"
run:
  kind: shell
  command: ["go", "test", "./..."]
`)
	s, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if s.Name != "nightly" {
		t.Fatalf("name: %q", s.Name)
	}
	if s.Trigger.Kind != TriggerKindCron || s.Trigger.Cron != "0 3 * * *" {
		t.Fatalf("trigger: %+v", s.Trigger)
	}
	if s.Run.Kind != specfmt.RecipeKindShell {
		t.Fatalf("run kind: %q", s.Run.Kind)
	}
	if got := s.Run.Command; len(got) != 3 || got[0] != "go" {
		t.Fatalf("command: %v", got)
	}
}

func TestLoadFileFileWatchOK(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeYAML(t, dir, "on-save", `
spec_version: 1
name: on-save
trigger:
  kind: file_watch
  paths:
    - "src/**/*.go"
  debounce_ms: 250
run:
  kind: shell
  command: ["go", "vet", "./..."]
`)
	s, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if s.Trigger.Kind != TriggerKindFileWatch {
		t.Fatalf("kind: %q", s.Trigger.Kind)
	}
	if s.Trigger.Debounce() != 250*1_000_000 {
		t.Fatalf("debounce: %v", s.Trigger.Debounce())
	}
}

func TestValidateRejectsBadShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body    string
		wantSub string
	}{
		"missing trigger": {`
spec_version: 1
name: x
run:
  kind: shell
  command: ["true"]
`, "trigger"},
		"unknown trigger kind": {`
spec_version: 1
name: x
trigger:
  kind: webhook
  cron: "0 0 * * *"
run:
  kind: shell
  command: ["true"]
`, "unknown trigger kind"},
		"cron without expr": {`
spec_version: 1
name: x
trigger:
  kind: cron
run:
  kind: shell
  command: ["true"]
`, "requires cron"},
		"file_watch without paths": {`
spec_version: 1
name: x
trigger:
  kind: file_watch
run:
  kind: shell
  command: ["true"]
`, "requires paths"},
		"missing run": {`
spec_version: 1
name: x
trigger:
  kind: cron
  cron: "0 0 * * *"
`, "run block is required"},
		"shell run without command": {`
spec_version: 1
name: x
trigger:
  kind: cron
  cron: "0 0 * * *"
run:
  kind: shell
`, "shell requires command"},
		"prompt token rejected": {`
spec_version: 1
name: x
trigger:
  kind: cron
  cron: "0 0 * * *"
run:
  kind: shell
  command: ["echo", "{{ task.id }}"]
`, "PROMPT.1"},
		"non-kebab name": {`
spec_version: 1
name: NotKebab
trigger:
  kind: cron
  cron: "0 0 * * *"
run:
  kind: shell
  command: ["true"]
`, "not kebab-case"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeYAML(t, dir, "x", tc.body)
			_, err := LoadFile(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestLoadFileNameMustMatchFilename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Name in YAML differs from file basename.
	path := writeYAML(t, dir, "actual", `
spec_version: 1
name: different
trigger:
  kind: cron
  cron: "0 0 * * *"
run:
  kind: shell
  command: ["true"]
`)
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestLoadDirSortsByName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, n := range []string{"zeta", "alpha", "middle"} {
		writeYAML(t, dir, n, `
spec_version: 1
name: `+n+`
trigger:
  kind: cron
  cron: "0 0 * * *"
run:
  kind: shell
  command: ["true"]
`)
	}
	out, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("count: %d", len(out))
	}
	got := []string{out[0].Name, out[1].Name, out[2].Name}
	want := []string{"alpha", "middle", "zeta"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sort: got %v want %v", got, want)
		}
	}
}

func TestLoadDirMissingDirOK(t *testing.T) {
	t.Parallel()
	out, err := LoadDir(filepath.Join(t.TempDir(), "no-such-dir"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %v", out)
	}
}

func TestFixedPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"src/**/*.go":   "src",
		"src/foo.go":    "src/foo.go",
		"a/b/c":         "a/b/c",
		"*.go":          ".",
		"":              "",
		"/abs/src/*.go": "/abs/src",
	}
	for in, want := range cases {
		if got := fixedPrefix(in); got != want {
			t.Errorf("fixedPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
