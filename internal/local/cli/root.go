package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	// Side-effect import: registers every bundled harness adapter
	// (execution.ADAPT.*) into adapter.Default() before any CLI
	// command runs.
	_ "github.com/asabla/rex/internal/core/runner/adapter/all"
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
		Example: `  rex workspace init
  rex spec --workspace /path/to/ws list
  rex run --workspace /path/to/ws start --shell "echo hello rex"
  rex serve --workspace /path/to/ws`,
		Version: version,
		// Don't auto-print usage on every error; cobra's default is
		// noisy. We handle errors at the leaf level.
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	setRelated(root,
		"rex status",
		"rex search <query>",
		"rex serve",
	)
	installRelatedHelp(root)

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
	root.AddCommand(newRepoCmd())
	root.AddCommand(newScheduleCmd())
	root.AddCommand(newSpecCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newHooksCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newPushCmd())
	root.AddCommand(newPullCmd())
	root.AddCommand(newRemoteCmd())
	root.AddCommand(newIdentityCmd())
	root.AddCommand(newSnapshotCmd())
	root.AddCommand(newLogCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newServeCmd(version))

	return root
}

// useColor reports whether the CLI should emit ANSI escape codes for
// w. Honours --no-color (cmd flag), NO_COLOR (env, per the de-facto
// no-color.org convention), and TERM=dumb. When w is not a *os.File
// pointing at a character device (i.e. a buffer in tests, or a pipe
// in scripts), returns false so output stays clean for grep/awk
// downstream.
func useColor(cmd *cobra.Command, w io.Writer) bool {
	noColor, _ := cmd.Flags().GetBool("no-color")
	if noColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
