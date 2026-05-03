package cli

import (
	"bytes"
	"strings"
	"testing"
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
	for _, want := range []string{"workspace", "spec", "run", "hooks", "status"} {
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

func TestStatusPlaceholder(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "not yet implemented") {
		t.Fatalf("status placeholder unexpected: %q", out)
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
