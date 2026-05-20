package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initAmendWorkspace builds a workspace and seeds the
// _proposed/ directory with a couple of amendment files.
func initAmendWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir,
		"--id", "amendws", "--name", "AmendWS",
		"--registry-file", filepath.Join(t.TempDir(), "reg.toml")); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

func writeProposed(t *testing.T, root, stem, target, date, summary string) string {
	t.Helper()
	dir := filepath.Join(root, ".rex", "specs", "_proposed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join([]string{
		"amendment_for: " + target,
		"amendment_date: " + date,
		"state: proposed",
		"summary: |",
		"  " + summary,
		"",
	}, "\n")
	path := filepath.Join(dir, stem+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", stem, err)
	}
	return path
}

func TestSpecAmendListEmpty(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	out, err := executeCommand(t, "spec", "amend", "list", "--workspace", dir)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no amendments found") {
		t.Errorf("expected empty-state message, got: %s", out)
	}
}

func TestSpecAmendListAndFilter(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	writeProposed(t, dir, "cli-amendment-2026-05-10", "cli", "2026-05-10", "extend amend")
	writeProposed(t, dir, "audit-amendment-2026-05-08", "audit", "2026-05-08", "older")

	out, err := executeCommand(t, "spec", "amend", "list", "--workspace", dir)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"cli-amendment-2026-05-10", "audit-amendment-2026-05-08", "STATE", "DATE", "FOR"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	out, err = executeCommand(t, "spec", "amend", "list", "--workspace", dir, "--for", "cli")
	if err != nil {
		t.Fatalf("list --for cli: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cli-amendment-2026-05-10") {
		t.Errorf("filter missing cli amendment: %s", out)
	}
	if strings.Contains(out, "audit-amendment-2026-05-08") {
		t.Errorf("filter should exclude audit amendment: %s", out)
	}
}

func TestSpecAmendShow(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	writeProposed(t, dir, "cli-amendment-2026-05-10", "cli", "2026-05-10", "extend amend")

	out, err := executeCommand(t, "spec", "amend", "show", "cli-amendment-2026-05-10", "--workspace", dir)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	for _, want := range []string{
		"stem:           cli-amendment-2026-05-10",
		"state:          proposed",
		"amendment_for:  cli",
		"amendment_date: 2026-05-10",
		"extend amend",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSpecAmendShowMissing(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	_, err := executeCommand(t, "spec", "amend", "show", "ghost-amendment-2026-05-10", "--workspace", dir)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSpecAmendAcceptHappyPath(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	src := writeProposed(t, dir, "cli-amendment-2026-05-10", "cli", "2026-05-10", "extend amend")

	out, err := executeCommand(t, "spec", "amend", "accept", "cli-amendment-2026-05-10", "--workspace", dir)
	if err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	if !strings.Contains(out, "accepted cli-amendment-2026-05-10") {
		t.Errorf("expected confirmation: %s", out)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
	dst := filepath.Join(dir, ".rex", "specs", "_proposed", "_accepted", "cli-amendment-2026-05-10.yaml")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !strings.Contains(string(body), "state: accepted") {
		t.Errorf("state not rewritten: %s", body)
	}
}

func TestSpecAmendAcceptAlreadyAccepted(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	acceptedDir := filepath.Join(dir, ".rex", "specs", "_proposed", "_accepted")
	if err := os.MkdirAll(acceptedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "amendment_for: cli\namendment_date: 2026-05-10\nstate: accepted\nsummary: x\n"
	if err := os.WriteFile(filepath.Join(acceptedDir, "cli-amendment-2026-05-10.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("seed accepted: %v", err)
	}
	_, err := executeCommand(t, "spec", "amend", "accept", "cli-amendment-2026-05-10", "--workspace", dir)
	if err == nil {
		t.Fatal("expected error on already-accepted amendment")
	}
	if !strings.Contains(err.Error(), "already accepted") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSpecAmendRejectHappyPath(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	src := writeProposed(t, dir, "cli-amendment-2026-05-10", "cli", "2026-05-10", "extend amend")

	out, err := executeCommand(t, "spec", "amend", "reject", "cli-amendment-2026-05-10", "--workspace", dir)
	if err != nil {
		t.Fatalf("reject: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rejected cli-amendment-2026-05-10") {
		t.Errorf("expected confirmation: %s", out)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present: %v", err)
	}
}

func TestSpecAmendRejectRefusesAccepted(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	acceptedDir := filepath.Join(dir, ".rex", "specs", "_proposed", "_accepted")
	if err := os.MkdirAll(acceptedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "amendment_for: cli\namendment_date: 2026-05-10\nstate: accepted\nsummary: x\n"
	if err := os.WriteFile(filepath.Join(acceptedDir, "cli-amendment-2026-05-10.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("seed accepted: %v", err)
	}
	_, err := executeCommand(t, "spec", "amend", "reject", "cli-amendment-2026-05-10", "--workspace", dir)
	if err == nil {
		t.Fatal("expected refusal on accepted amendment")
	}
}

// TestSpecAmendBareFormDeprecation exercises the legacy alias path:
// calling `spec amend <id> [prompt] --harness <bad>` should print
// the deprecation warning AND still surface the harness error so
// existing automation doesn't break silently.
func TestSpecAmendBareFormDeprecation(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	writeAdHocSpec(t, dir, "subject", "Subject")

	out, err := executeCommand(t, "spec", "amend", "subject", "tighten",
		"--workspace", dir, "--harness", "imaginary")
	if err == nil {
		t.Fatalf("expected harness error: %s", out)
	}
	if !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("expected unknown-harness error, got: %v", err)
	}
	if !strings.Contains(out, "deprecated") {
		t.Errorf("expected deprecation warning in stderr/stdout, got: %s", out)
	}
}

// TestSpecAmendDraftIsValid confirms the new draft subcommand
// surface accepts the same flag shape as the bare alias.
func TestSpecAmendDraftIsValid(t *testing.T) {
	t.Parallel()
	dir := initAmendWorkspace(t)
	writeAdHocSpec(t, dir, "subject", "Subject")

	_, err := executeCommand(t, "spec", "amend", "draft", "subject", "tighten",
		"--workspace", dir, "--harness", "imaginary")
	if err == nil {
		t.Fatal("expected unknown-harness error")
	}
	if !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("wrong error: %v", err)
	}
}
