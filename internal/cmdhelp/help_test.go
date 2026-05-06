package cmdhelp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRelatedCommandsUseExplicitOverride(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "child"}
	SetRelated(cmd, "rex run attach", "rex run list")
	got := RelatedCommands(cmd)
	if len(got) != 2 || got[0] != "rex run attach" || got[1] != "rex run list" {
		t.Fatalf("RelatedCommands explicit: %v", got)
	}
}

func TestRelatedCommandsFallBackToChildren(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "rex"}
	root.AddCommand(&cobra.Command{Use: "status", Short: "s"})
	root.AddCommand(&cobra.Command{Use: "search", Short: "s"})
	got := RelatedCommands(root)
	if len(got) != 2 {
		t.Fatalf("RelatedCommands children len: %v", got)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "rex status") || !strings.Contains(joined, "rex search") {
		t.Fatalf("RelatedCommands children: %v", got)
	}
}

func TestRelatedCommandsFallBackToSiblingsAndParent(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "rex"}
	alpha := &cobra.Command{Use: "alpha", Short: "a"}
	beta := &cobra.Command{Use: "beta", Short: "b"}
	gamma := &cobra.Command{Use: "gamma", Short: "g"}
	root.AddCommand(alpha, beta, gamma)
	got := RelatedCommands(beta)
	if len(got) != 3 {
		t.Fatalf("RelatedCommands siblings len: %v", got)
	}
	if got[0] != "rex alpha" || got[1] != "rex gamma" || got[2] != "rex" {
		t.Fatalf("RelatedCommands siblings: %v", got)
	}
}

func TestInstallRelatedHelpAppendsSection(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "rex", Short: "root", Long: "root long", Example: "rex"}
	child := &cobra.Command{Use: "status", Short: "status", Long: "status long", Example: "rex status"}
	root.AddCommand(child)
	SetRelated(child, "rex search", "rex log")
	InstallRelatedHelp(root)

	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"status", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	if !strings.Contains(buf.String(), "Related Commands:") {
		t.Fatalf("help missing related section: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "rex search") {
		t.Fatalf("help missing related command: %s", buf.String())
	}
}
