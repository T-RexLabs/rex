package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// executeCommand runs the root command tree against the given args and
// returns whatever was written to either stdout or stderr.
func executeCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return executeCommandVersion(t, "test", args...)
}

func executeCommandVersion(t *testing.T, version string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd(version)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestRootHelpListsTopLevelCommands(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "--help")
	if err != nil {
		t.Fatalf("Execute --help: %v", err)
	}
	for _, want := range []string{"init", "workspace", "spec", "run", "hooks", "status"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help missing %q\n%s", want, out)
		}
	}
}

func TestRootVersionPrintsVersion(t *testing.T) {
	t.Parallel()

	out, err := executeCommandVersion(t, "v1-test", "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out, "v1-test") {
		t.Fatalf("--version output: %q", out)
	}
}

func TestNewRootCmdSubstitutesEmptyVersion(t *testing.T) {
	t.Parallel()

	out, err := executeCommandVersion(t, "", "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out, DefaultVersion) {
		t.Fatalf("default version not substituted: %q", out)
	}
}

func TestStatusHelpListsFlags(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "status", "--help")
	if err != nil {
		t.Fatalf("status --help: %v", err)
	}
	if !strings.Contains(out, "--workspace") {
		t.Fatalf("status help missing --workspace flag: %s", out)
	}
}

func TestSubcommandHelp(t *testing.T) {
	t.Parallel()

	for _, leaf := range []string{"workspace", "spec", "run", "hooks"} {
		t.Run(leaf, func(t *testing.T) {
			out, err := executeCommand(t, leaf, "--help")
			if err != nil {
				t.Fatalf("%s --help: %v", leaf, err)
			}
			if !strings.Contains(out, leaf) {
				t.Fatalf("help output should mention %s: %q", leaf, out)
			}
		})
	}
}

func TestUnknownCommandFails(t *testing.T) {
	t.Parallel()

	_, err := executeCommand(t, "fly")
	if err == nil {
		t.Fatal("unknown command should error")
	}
}

func TestVisibleCommandsHaveLongAndExamples(t *testing.T) {
	t.Parallel()

	assertCommandHelpShape(t, NewRootCmd("test"))
}

func TestHelpIncludesExamplesSection(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "spec", "create", "--help")
	if err != nil {
		t.Fatalf("spec create --help: %v", err)
	}
	if !strings.Contains(out, "Examples:") {
		t.Fatalf("help output missing Examples section: %s", out)
	}
}

func TestHelpIncludesRelatedCommandsSection(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "run", "start", "--help")
	if err != nil {
		t.Fatalf("run start --help: %v", err)
	}
	if !strings.Contains(out, "Related Commands:") {
		t.Fatalf("help output missing Related Commands section: %s", out)
	}
	if !strings.Contains(out, "rex run attach <run-id>") {
		t.Fatalf("help output missing expected related command: %s", out)
	}
	if !strings.Contains(out, "rex run list") {
		t.Fatalf("help output missing expected related command: %s", out)
	}
}

func assertCommandHelpShape(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	assertOneCommandHelpShape(t, cmd)
	for _, sub := range cmd.Commands() {
		if sub.Hidden || !sub.IsAvailableCommand() || sub.Name() == "help" {
			continue
		}
		assertCommandHelpShape(t, sub)
	}
}

func assertOneCommandHelpShape(t *testing.T, cmd *cobra.Command) {
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
