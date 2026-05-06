package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootHelpListsServe(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	if !strings.Contains(buf.String(), "serve") {
		t.Fatalf("help output missing serve: %s", buf.String())
	}
}

func TestRootVersionPrintsVersion(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("v9-test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(buf.String(), "v9-test") {
		t.Fatalf("--version output: %s", buf.String())
	}
}

func TestServeHelpListsFlags(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"serve", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("serve --help: %v", err)
	}
	if !strings.Contains(buf.String(), "--addr") {
		t.Fatalf("serve help missing --addr: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "--shutdown-timeout") {
		t.Fatalf("serve help missing --shutdown-timeout: %s", buf.String())
	}
}

func TestVisibleCommandsHaveLongAndExamples(t *testing.T) {
	t.Parallel()

	assertCentralCommandHelpShape(t, newRootCmd("test"))
}

func TestRestoreHelpIncludesExamplesSection(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"restore", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore --help: %v", err)
	}
	if !strings.Contains(buf.String(), "Examples:") {
		t.Fatalf("help output missing Examples section: %s", buf.String())
	}
}

func TestHelpIncludesRelatedCommandsSection(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"serve", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("serve --help: %v", err)
	}
	if !strings.Contains(buf.String(), "Related Commands:") {
		t.Fatalf("help output missing Related Commands section: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "rex-central backup") {
		t.Fatalf("help output missing expected related command: %s", buf.String())
	}
}

func assertCentralCommandHelpShape(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	assertOneCentralCommandHelpShape(t, cmd)
	for _, sub := range cmd.Commands() {
		if sub.Hidden || !sub.IsAvailableCommand() || sub.Name() == "help" {
			continue
		}
		assertCentralCommandHelpShape(t, sub)
	}
}

func assertOneCentralCommandHelpShape(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	if strings.TrimSpace(cmd.Short) == "" {
		t.Fatalf("command %q missing Short help", cmd.CommandPath())
	}
	if strings.TrimSpace(cmd.Long) == "" {
		t.Fatalf("command %q missing Long help", cmd.CommandPath())
	}
	if strings.TrimSpace(cmd.Example) == "" {
		t.Fatalf("command %q missing Example help", cmd.CommandPath())
	}
}
