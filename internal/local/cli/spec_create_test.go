package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initSpecCreateWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "workspace", "init", dir, "--id", "scw", "--name", "SCW"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

func TestSpecCreateMinimalSkeleton(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	out, err := executeCommand(t, "spec", "create", "alpha", "--workspace", dir)
	if err != nil {
		t.Fatalf("spec create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "minimal skeleton") {
		t.Fatalf("output should report skeleton: %s", out)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".rex", "specs", "alpha.yaml"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	// Identity fields stay required; the rest are the rich
	// placeholders the minimal scaffold path now ships so authors
	// don't have to discover the schema in spec-format.yaml.
	for _, want := range []string{
		"id: alpha",
		"name: alpha",
		"state: draft",
		"owners: []",
		"related_specs: []",
		"description: |",
		"tasks:",
		"id: example-task",
		"note: \"\"",
		"proof: []",
		"depends_on: []",
		"components:",
		"EXAMPLE:",
		"# proof:",
		"# depends_on:",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestSpecCreateWithExplicitTemplate(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	// Drop a template into specs/ first.
	tplPath := filepath.Join(dir, ".rex", "specs", "api-template.yaml")
	if err := os.WriteFile(tplPath, []byte(`spec_version: 1
metadata:
  id: api-template
  name: API spec template
  state: active
description: |
  Template for API specs.
tasks:
  - id: design
    description: Sketch the API surface
    state: todo
components:
  API:
    name: API surface
    requirements:
      "1": Define the resource hierarchy
extra:
  template: true
  applies_to: workspace
  required_extra: [owner]
`), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	out, err := executeCommand(t, "spec", "create", "my-api",
		"--template", "api-template",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("spec create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "api-template") {
		t.Fatalf("output should mention template: %s", out)
	}

	body, err := os.ReadFile(filepath.Join(dir, ".rex", "specs", "my-api.yaml"))
	if err != nil {
		t.Fatalf("read created spec: %v", err)
	}
	bodyStr := string(body)
	for _, want := range []string{"id: my-api", "components:", "API:", "template_id: api-template"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("missing %q in:\n%s", want, bodyStr)
		}
	}
	// Template marker keys should be stripped.
	for _, banned := range []string{"template: true", "applies_to:", "required_extra:"} {
		if strings.Contains(bodyStr, banned) {
			t.Errorf("scaffolded spec leaked %q:\n%s", banned, bodyStr)
		}
	}
}

func TestSpecCreateRefusesOverwriteWithoutForce(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	if _, err := executeCommand(t, "spec", "create", "alpha", "--workspace", dir); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := executeCommand(t, "spec", "create", "alpha", "--workspace", dir)
	if err == nil {
		t.Fatal("second create without --force should error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestSpecCreateForceOverwrites(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	if _, err := executeCommand(t, "spec", "create", "alpha",
		"--workspace", dir, "--name", "First",
	); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := executeCommand(t, "spec", "create", "alpha",
		"--workspace", dir, "--name", "Second", "--force",
	); err != nil {
		t.Fatalf("force create: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".rex", "specs", "alpha.yaml"))
	if !strings.Contains(string(body), "name: Second") {
		t.Fatalf("force did not overwrite: %s", body)
	}
}

func TestSpecCreateRejectsBadID(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	_, err := executeCommand(t, "spec", "create", "Bad Id", "--workspace", dir)
	if err == nil {
		t.Fatal("expected kebab-case rejection")
	}
}

func TestSpecCreateUnknownTemplate(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	_, err := executeCommand(t, "spec", "create", "my-api",
		"--template", "ghost",
		"--workspace", dir,
	)
	if err == nil {
		t.Fatal("unknown template should error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestSpecCreateWorkspaceDefaultTemplate(t *testing.T) {
	t.Parallel()

	dir := initSpecCreateWorkspace(t)
	tplPath := filepath.Join(dir, ".rex", "specs", "default-tpl.yaml")
	if err := os.WriteFile(tplPath, []byte(`spec_version: 1
metadata:
  id: default-tpl
  name: Default
  state: active
extra:
  template: true
`), 0o644); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	// Patch workspace.yaml to set default_template_id.
	wsPath := filepath.Join(dir, ".rex", "workspace.yaml")
	body, _ := os.ReadFile(wsPath)
	patched := string(body) + "extra:\n  default_template_id: default-tpl\n"
	if err := os.WriteFile(wsPath, []byte(patched), 0o644); err != nil {
		t.Fatalf("patch workspace.yaml: %v", err)
	}

	out, err := executeCommand(t, "spec", "create", "from-default",
		"--workspace", dir,
	)
	if err != nil {
		t.Fatalf("spec create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "default-tpl") {
		t.Fatalf("output should pick up default template: %s", out)
	}
	created, _ := os.ReadFile(filepath.Join(dir, ".rex", "specs", "from-default.yaml"))
	if !strings.Contains(string(created), "template_id: default-tpl") {
		t.Fatalf("scaffolded spec should reference default template: %s", created)
	}
}
