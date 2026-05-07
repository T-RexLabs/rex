package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/core/runner/primharness"
	"github.com/asabla/rex/internal/core/runner/primshell"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/runtask"
)

// newRunCmd returns the `rex run` parent and wires its leaves.
func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start, watch, and manage runs",
		Long: `A run is one execution of a workflow DAG against a harness. See
specs/execution.yaml for the run lifecycle.

V1 daily-drive ships start (shell-only), list, and show. The
harness-driven flags from cli.RUN.1 (--harness, --prompt, --spec)
land once the harness adapter registry exists; cancel/watch/signal
need a daemon model that v1 does not have.`,
		Example: `  rex run start --shell "echo hello"
  rex run list
  rex run attach curious-giraffe`,
	}
	setRelated(cmd,
		"rex run start --shell \"echo hello\"",
		"rex run list",
		"rex run attach <run-id>",
	)
	addWorkspacePersistentFlag(cmd)
	cmd.AddCommand(newRunStartCmd())
	cmd.AddCommand(newRunAttachCmd())
	cmd.AddCommand(newRunWatchAliasCmd())
	cmd.AddCommand(newRunListCmd())
	cmd.AddCommand(newRunShowCmd())
	return cmd
}

// eventLogPath returns the canonical events.log path for a workspace.
func eventLogPath(workspaceRoot string) string {
	return runtask.EventLogPath(workspaceRoot)
}

// runDecoderRegistry returns an event.Registry that knows how to
// decode every runner event type. Used by list, show, and attach to
// read the log without each command rebuilding the table.
func runDecoderRegistry() *event.Registry {
	r := event.NewRegistry()
	runner.RegisterEvents(r)
	return r
}

// openReaderForPath wraps eventlog.OpenReader to return (nil, nil) when
// the file does not exist — the natural pre-first-run state.
func openReaderForPath(path string) (*eventlog.Reader, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return eventlog.OpenReader(path)
}

func newRunStartCmd() *cobra.Command {
	var (
		shellCommand string
		harnessFlag  string
		promptFlag   string
		modelFlag    string
		modeFlag     string
		timeoutFlag  time.Duration
		nodeID       string
		runIDFlag    string
		quietFlag    bool
		detachFlag   bool
		fromTaskFlag string
		specRefFlags []string
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a run",
		Long: `Start a single-node DAG run.

Two flavors:
  --shell <cmd>             one-shot shell_exec
  --harness <name> --prompt one-shot harness_invocation against an
                            adapter registered via the harness adapter
                            registry (cli.RUN.1, execution.ADAPT.*)

The default invocation is attached: events stream to the terminal
during execution and the command exits when the run terminates.
--quiet suppresses the live stream (final summary only).
		--detach is reserved for backgrounded runs and is not yet wired —
		v1 has no daemon model, so the run's lifetime is the CLI process.

		To re-attach later, run 'rex run attach <run-id>'.`,
		Example: `  rex run start --shell "echo hello"
  rex run --workspace /path/to/ws start --shell "make test"
  rex run start --harness claude-code --prompt "summarize this repo"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if detachFlag {
				return errors.New("--detach is deferred until backgrounding/daemon support lands (cli.RUN.1)")
			}
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}

			ws, err := runtask.Open(root)
			if err != nil {
				return err
			}
			defer ws.Close()

			ctx := commandContext(cmd)

			onEvent := liveEventPrinter(cmd, quietFlag)

			// --from-task: resolve the recipe and dispatch to the
			// matching shape (execution.RUN.1.1, spec-format.RECIPE.*).
			if fromTaskFlag != "" {
				resolved, err := resolveTaskRecipe(root, fromTaskFlag, specRefFlags...)
				if err != nil {
					return err
				}
				switch resolved.Recipe.Kind {
				case specfmt.RecipeKindHarness:
					if _, ok := adapter.Default().Lookup(resolved.Recipe.Harness); !ok {
						return fmt.Errorf("recipe references harness %q which has no adapter registered (registered: %s)",
							resolved.Recipe.Harness, strings.Join(adapter.Default().Names(), ", "))
					}
					node := nodeID
					if node == "" || node == "shell" {
						node = "harness"
					}
					res, err := runtask.StartHarnessRun(ctx, ws, runtask.HarnessRunRequest{
						Harness:  resolved.Recipe.Harness,
						Prompt:   resolved.Prompt,
						Timeout:  timeoutFlag,
						NodeID:   node,
						RunID:    runIDFlag,
						SpecRefs: resolved.SpecRefs,
						FromTask: resolved.FromTask,
						OnEvent:  onEvent,
						OnStderr: harnessStderrPrinter(cmd, quietFlag),
					})
					if err != nil {
						return err
					}
					return reportHarnessRun(cmd, res, node)
				case specfmt.RecipeKindShell:
					res, err := runtask.StartShellRun(ctx, ws, runtask.ShellRunRequest{
						Command:  resolved.Command,
						Dir:      recipeWorkspaceDir(root, resolved.Recipe.Cwd),
						Env:      resolved.Recipe.Env,
						NodeID:   nodeID,
						RunID:    runIDFlag,
						SpecRefs: resolved.SpecRefs,
						FromTask: resolved.FromTask,
						OnEvent:  onEvent,
					})
					if err != nil {
						return err
					}
					return reportShellRun(cmd, res, nodeID)
				}
				return fmt.Errorf("recipe kind %q is not yet supported by `rex run start --from-task`", resolved.Recipe.Kind)
			}

			specRefs := dedupeRefs(specRefFlags)
			if harnessFlag != "" {
				if _, ok := adapter.Default().Lookup(harnessFlag); !ok {
					return fmt.Errorf("no adapter registered for %q (registered: %s)",
						harnessFlag, strings.Join(adapter.Default().Names(), ", "))
				}
				node := nodeID
				if node == "" || node == "shell" {
					node = "harness"
				}
				res, err := runtask.StartHarnessRun(ctx, ws, runtask.HarnessRunRequest{
					Harness:  harnessFlag,
					Prompt:   promptFlag,
					Model:    modelFlag,
					Mode:     modeFlag,
					Timeout:  timeoutFlag,
					NodeID:   node,
					RunID:    runIDFlag,
					SpecRefs: specRefs,
					OnEvent:  onEvent,
					OnStderr: harnessStderrPrinter(cmd, quietFlag),
				})
				if err != nil {
					return err
				}
				return reportHarnessRun(cmd, res, node)
			}

			argv, err := runtask.SplitShellCommand(shellCommand)
			if err != nil {
				return err
			}
			res, err := runtask.StartShellRun(ctx, ws, runtask.ShellRunRequest{
				Command:  argv,
				NodeID:   nodeID,
				RunID:    runIDFlag,
				SpecRefs: specRefs,
				OnEvent:  onEvent,
			})
			if err != nil {
				return err
			}
			return reportShellRun(cmd, res, nodeID)
		},
	}
	setRelated(cmd,
		"rex run attach <run-id>",
		"rex run list",
		"rex run show <run-id>",
	)
	cmd.Flags().StringVar(&shellCommand, "shell", "", "shell command to execute as the only DAG node")
	cmd.Flags().StringVar(&harnessFlag, "harness", "", "registered harness adapter name (e.g. claude-code)")
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "initial user message for --harness")
	cmd.Flags().StringVar(&modelFlag, "model", "", "model name (passed through to the adapter)")
	cmd.Flags().StringVar(&modeFlag, "mode", "", "mode (passed through to the adapter)")
	cmd.Flags().DurationVar(&timeoutFlag, "timeout", 0, "harness invocation timeout (default: no timeout)")
	cmd.Flags().StringVar(&nodeID, "node-id", "shell", "id assigned to the only DAG node")
	cmd.Flags().StringVar(&runIDFlag, "run-id", "", "explicit run id (default: HLC-derived; useful for tests)")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "suppress the live event stream; print only the final summary")
	cmd.Flags().BoolVarP(&detachFlag, "detach", "d", false, "(reserved) kick off the run and return immediately — not yet wired")
	cmd.Flags().Bool("debug", false, "render full event payloads instead of one-line summaries")
	cmd.Flags().StringVar(&fromTaskFlag, "from-task", "", "load a recipe from <spec-id>.<task-id> and prefill --harness/--prompt/--shell from it (execution.RUN.1.1)")
	cmd.Flags().StringSliceVar(&specRefFlags, "spec-ref", nil, "fully-qualified ACID this run satisfies; may be repeated (execution.RUN.1.1)")
	cmd.MarkFlagsOneRequired("shell", "harness", "from-task")
	cmd.MarkFlagsMutuallyExclusive("shell", "harness", "from-task")
	cmd.MarkFlagsRequiredTogether("harness", "prompt")
	return cmd
}

// harnessStderrPrinter routes the harness's stderr lines to the
// CLI's stderr, prefixed so the user can tell at a glance which
// output came from the bridge versus rex itself. Returns nil in
// --quiet so script-driven invocations stay clean; --json never
// sees these lines.
func harnessStderrPrinter(cmd *cobra.Command, quiet bool) func(string) {
	if quiet {
		return nil
	}
	errOut := cmd.ErrOrStderr()
	return func(line string) {
		fmt.Fprintf(errOut, "[harness stderr] %s\n", line)
	}
}

// liveEventPrinter returns a runtask.OnEvent callback that renders
// each event in the same one-line format `rex run attach` uses.
// Returns nil when the JSON-output flag is set OR --quiet is in
// effect, so the final summary is the only thing on stdout in
// those modes. In --debug mode the full payload is printed
// indented under each event header instead of the one-line
// summary, matching the web UI's debug toggle (web-ui.LIVE.*).
func liveEventPrinter(cmd *cobra.Command, quiet bool) func(eventlog.Record) {
	if quiet {
		return nil
	}
	if jsonOutput(cmd) {
		return nil
	}
	debug, _ := cmd.Flags().GetBool("debug")
	return newStreamPrinter(cmd.OutOrStdout(), debug)
}

// reportShellRun writes the human-friendly tail block for a shell
// run; used to keep newRunStartCmd readable now that it branches.
func reportShellRun(cmd *cobra.Command, res *runtask.ShellRunResult, nodeID string) error {
	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
			"run_id": res.RunID,
			"status": string(res.State.Status),
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "run %s (%s): %s\n",
		runner.FriendlyName(res.RunID), res.RunID, res.State.Status)
	node := res.State.Nodes[runner.NodeID(nodeID)]
	if node != nil && len(node.Output) > 0 {
		var out primshell.Output
		if err := json.Unmarshal(node.Output, &out); err == nil {
			if strings.TrimSpace(out.Stdout) != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "stdout:\n%s", out.Stdout)
				if !strings.HasSuffix(out.Stdout, "\n") {
					fmt.Fprintln(cmd.OutOrStdout())
				}
			}
			if strings.TrimSpace(out.Stderr) != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "stderr:\n%s", out.Stderr)
				if !strings.HasSuffix(out.Stderr, "\n") {
					fmt.Fprintln(cmd.ErrOrStderr())
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "exit: %d  duration: %s\n", out.ExitCode, out.Duration)
		}
	}
	if res.State.Status != runner.RunStatusCompleted {
		return fmt.Errorf("run %s ended in status %s", res.RunID, res.State.Status)
	}
	return nil
}

func reportHarnessRun(cmd *cobra.Command, res *runtask.ShellRunResult, nodeID string) error {
	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
			"run_id": res.RunID,
			"status": string(res.State.Status),
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "run %s (%s): %s\n",
		runner.FriendlyName(res.RunID), res.RunID, res.State.Status)
	node := res.State.Nodes[runner.NodeID(nodeID)]
	if node != nil && len(node.Output) > 0 {
		var out primharness.Output
		if err := json.Unmarshal(node.Output, &out); err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "session: %s  frames: %d  exit: %d  duration: %s\n",
				out.SessionID, out.FrameCount, out.ExitCode, time.Duration(out.Duration))
		}
	}
	if res.State.Status != runner.RunStatusCompleted {
		return fmt.Errorf("run %s ended in status %s", res.RunID, res.State.Status)
	}
	return nil
}

// splitShellCommand parses a --shell argument into argv. For v1 we
// accept POSIX-ish quoted strings: bare words and double-quoted runs
// of arbitrary characters. Single-quoted runs and backslash escapes
// can land later if usage demands; the function rejects unbalanced
// quotes loudly so the user is not silently misinterpreted.
//
// Production code paths use runtask.SplitShellCommand; this local
// copy stays for the existing run_test.go cases.
func splitShellCommand(cmd string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range cmd {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, errors.New("unbalanced \" in --shell")
	}
	flush()
	return out, nil
}
