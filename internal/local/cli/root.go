package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"
)

// DefaultVersion is the version string used when the binary did not
// inject one via -ldflags. Tests should pass their own version to
// NewRootCmd rather than touching this constant.
const DefaultVersion = "dev"

// Execute runs the rex CLI with os.Args using version as the
// reportable version. It is the entry point cmd/rex calls; tests
// build a root command via NewRootCmd directly.
func Execute(version string) int {
	if version == "" {
		version = DefaultVersion
	}
	cmd := NewRootCmd(version)
	if err := cmd.Execute(); err != nil {
		// cobra has already printed the error to stderr.
		return 1
	}
	return 0
}

// NewRootCmd builds a fresh `rex` command tree. Each call returns an
// isolated tree so tests can run in parallel without sharing state.
func NewRootCmd(version string) *cobra.Command {
	if version == "" {
		version = DefaultVersion
	}
	root := &cobra.Command{
		Use:   "rex",
		Short: "Rex — a management portal for agentic coding harnesses",
		Long: `Rex orchestrates agentic coding harnesses (Claude Code, Codex,
OpenCode, ...) over a local-first, optionally-replicated workspace
model. The CLI is verb-noun and deeply nested; see "rex help <noun>"
for each command group.`,
		Version: version,
		// Don't auto-print usage on every error; cobra's default is
		// noisy. We handle errors at the leaf level.
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	// Global flags. Each leaf command honours them (or ignores them
	// when not applicable); the parsing and storage live here so the
	// behaviour is consistent across the surface.
	root.PersistentFlags().Bool("json", false, "render structured output as newline-delimited JSON")
	root.PersistentFlags().Bool("quiet", false, "suppress progress and non-essential output")
	root.PersistentFlags().Bool("no-color", false, "disable ANSI colour output")

	// Subcommand groups. Leaves are wired in their own files; the
	// parents are introduced here so the tree shape is visible at a
	// glance and `rex --help` lists them in a stable order.
	root.AddCommand(newWorkspaceCmd())
	root.AddCommand(newSpecCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newHooksCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newPushCmd())
	root.AddCommand(newPullCmd())
	root.AddCommand(newRemoteCmd())
	root.AddCommand(newIdentityCmd())

	return root
}

// runWithOutput is a tiny helper that pipes stdout/stderr through cobra's
// configured writers. cobra's commands write to its OutOrStdout /
// ErrOrStderr; this wrapper makes leaf code less verbose.
type writers struct {
	Out io.Writer
	Err io.Writer
}

func cmdWriters(cmd *cobra.Command) writers {
	return writers{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
}

// orStdout returns w when non-nil, else os.Stdout. Lets tests inject
// buffers when constructing commands directly without going through a
// root command.
func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}
