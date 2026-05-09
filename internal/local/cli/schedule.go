// Package cli — `rex schedule` command tree (execution.SCHED.* /
// cli.SCHED.*).
//
// `rex schedule list`              list schedules
// `rex schedule show <name>`       pretty-print one
// `rex schedule add <name>`        scaffold + open in $EDITOR
// `rex schedule remove <name>`     delete the file
// `rex schedule trigger <name>`    fire one schedule once (test path)
// `rex schedule run`               foreground daemon
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/schedule"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/local/runtask"
)

// newScheduleCmd returns the `rex schedule` parent.
func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Define and run scheduled work (cron + file-watch)",
		Long: `Manage scheduled work for the workspace. v1 ships cron and file-watch
triggers (execution.SCHED.1.4). Webhook triggers depend on central-side
execution and are deferred to v1.5.

Schedule definitions live in .rex/schedules/<name>.yaml (git-merged)
with the shape pinned by execution.SCHED.2.1. The foreground daemon
('rex schedule run') keeps cron tickers and fsnotify watchers alive
and dispatches runs through the same executor every other run uses.`,
		Example: `  rex schedule add nightly --cron "0 3 * * *"
  rex schedule list
  rex schedule trigger nightly
  rex schedule run`,
	}
	setRelated(cmd,
		"rex schedule add <name>",
		"rex schedule list",
		"rex schedule run",
	)
	cmd.AddCommand(newScheduleListCmd())
	cmd.AddCommand(newScheduleShowCmd())
	cmd.AddCommand(newScheduleAddCmd())
	cmd.AddCommand(newScheduleRemoveCmd())
	cmd.AddCommand(newScheduleTriggerCmd())
	cmd.AddCommand(newScheduleRunCmd())
	return cmd
}

func newScheduleListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List schedules in .rex/schedules/",
		Long: `Walks the workspace's .rex/schedules/ directory and prints one row
per valid schedule with name, trigger kind, and a short summary of
the trigger configuration.`,
		Example: `  rex schedule list
  rex schedule list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			scheds, err := schedule.LoadDir(schedule.Dir(root))
			if err != nil {
				return err
			}
			if jsonOutput(cmd) {
				out := make([]map[string]any, 0, len(scheds))
				for _, s := range scheds {
					out = append(out, scheduleSummaryJSON(s))
				}
				return writeJSON(cmd, out)
			}
			if len(scheds) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no schedules")
				return nil
			}
			for _, s := range scheds {
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-12s %s\n",
					s.Name, string(s.Trigger.Kind), summarizeTrigger(s.Trigger))
			}
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex schedule show <name>", "rex schedule add <name>")
	return cmd
}

func newScheduleShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Pretty-print one schedule's YAML",
		Long:  `Reads .rex/schedules/<name>.yaml and prints the parsed schedule.`,
		Example: `  rex schedule show nightly
  rex schedule show nightly --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			s, err := schedule.LoadFile(schedule.FilePath(root, args[0]))
			if err != nil {
				return err
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, scheduleFullJSON(s))
			}
			body, err := os.ReadFile(s.Path)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s", body)
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex schedule list", "rex schedule trigger <name>")
	return cmd
}

func newScheduleAddCmd() *cobra.Command {
	var (
		cronExpr   string
		paths      []string
		shellCmd   string
		harness    string
		prompt     string
		openEditor bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Scaffold a new schedule file",
		Long: `Writes a minimal-valid schedule YAML at .rex/schedules/<name>.yaml.
Exactly one of --cron / --paths is required (chooses trigger kind).
The run block defaults to a no-op shell so the file validates; pass
--shell, or --harness + --prompt to fill it in. Use --edit to open
the file in $EDITOR after scaffolding.`,
		Example: `  rex schedule add nightly --cron "0 3 * * *" --shell "go test ./..."
  rex schedule add on-save --paths "src/**/*.go" --shell "go vet ./..."`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !specfmt.IsKebab(name) {
				return fmt.Errorf("schedule name %q is not kebab-case", name)
			}
			if cronExpr == "" && len(paths) == 0 {
				return errors.New("either --cron or --paths is required")
			}
			if cronExpr != "" && len(paths) > 0 {
				return errors.New("--cron and --paths are mutually exclusive")
			}

			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}

			path := schedule.FilePath(root, name)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", path, err)
			}

			s := schedule.Schedule{Name: name}
			switch {
			case cronExpr != "":
				s.Trigger = schedule.Trigger{Kind: schedule.TriggerKindCron, Cron: cronExpr}
			default:
				s.Trigger = schedule.Trigger{Kind: schedule.TriggerKindFileWatch, Paths: paths}
			}
			s.Run = buildScaffoldRecipe(shellCmd, harness, prompt)
			if err := s.Validate(); err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
			}
			body, err := yaml.Marshal(struct {
				SpecVersion int              `yaml:"spec_version"`
				Name        string           `yaml:"name"`
				Trigger     schedule.Trigger `yaml:"trigger"`
				Run         *specfmt.Recipe  `yaml:"run"`
			}{1, s.Name, s.Trigger, s.Run})
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}

			wsID, err := workspaceID(root)
			if err != nil {
				return err
			}
			if err := emitAuditEvent(cmd, root, audit.EventTypeScheduleAdded, audit.ScheduleAddedEvent{
				WorkspaceID: wsID,
				Name:        s.Name,
				Path:        path,
				TriggerKind: string(s.Trigger.Kind),
			}); err != nil {
				return err
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name": s.Name, "path": path, "trigger_kind": s.Trigger.Kind,
				})
			}
			printConfirmation(cmd, "added schedule %q at %s\n", s.Name, path)

			if openEditor {
				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "vi"
				}
				ed := exec.CommandContext(commandContext(cmd), editor, path)
				ed.Stdin = os.Stdin
				ed.Stdout = cmd.OutOrStdout()
				ed.Stderr = cmd.ErrOrStderr()
				if err := ed.Run(); err != nil {
					return fmt.Errorf("editor: %w", err)
				}
			}
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.Flags().StringVar(&cronExpr, "cron", "", "5-field cron expression (mutually exclusive with --paths)")
	cmd.Flags().StringSliceVar(&paths, "paths", nil, "file-watch globs (mutually exclusive with --cron)")
	cmd.Flags().StringVar(&shellCmd, "shell", "", "scaffold a shell recipe with this command")
	cmd.Flags().StringVar(&harness, "harness", "", "scaffold a harness recipe with this adapter")
	cmd.Flags().StringVar(&prompt, "prompt", "", "scaffold a harness recipe with this prompt (requires --harness)")
	cmd.Flags().BoolVar(&openEditor, "edit", false, "open the new file in $EDITOR after writing")
	setRelated(cmd, "rex schedule list", "rex schedule trigger <name>")
	return cmd
}

func newScheduleRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Short:   "Delete a schedule file",
		Long:    `Deletes .rex/schedules/<name>.yaml. The daemon (if running) does not pick up the removal until restart.`,
		Example: `  rex schedule remove nightly`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			path := schedule.FilePath(root, args[0])
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("no schedule named %q", args[0])
				}
				return err
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove %s: %w", path, err)
			}
			wsID, err := workspaceID(root)
			if err != nil {
				return err
			}
			if err := emitAuditEvent(cmd, root, audit.EventTypeScheduleRemoved, audit.ScheduleRemovedEvent{
				WorkspaceID: wsID,
				Name:        args[0],
				Path:        path,
			}); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{"name": args[0], "path": path})
			}
			printConfirmation(cmd, "removed schedule %q\n", args[0])
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex schedule list", "rex schedule add <name>")
	return cmd
}

func newScheduleTriggerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger <name>",
		Short: "Fire one schedule once and exit",
		Long: `Resolves the schedule by name and fires its run synchronously,
without starting the daemon. Useful for testing a schedule's run
block before relying on cron/file-watch dispatch.`,
		Example: `  rex schedule trigger nightly`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			s, err := schedule.LoadFile(schedule.FilePath(root, args[0]))
			if err != nil {
				return err
			}
			ctx := commandContext(cmd)
			dispatch := buildScheduleDispatcher(cmd, root)
			return schedule.FireOnce(ctx, dispatch, s, time.Now)
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex schedule run", "rex schedule list")
	return cmd
}

func newScheduleRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the foreground schedule daemon",
		Long: `Starts the v1 schedule daemon (execution.SCHED.2.2). Walks
.rex/schedules/, registers cron tickers and fsnotify watchers,
and dispatches runs through the same DAG executor every other run
uses. Concurrency is workspace-serial per EXEC-CONC.1 — fires that
arrive while a run is in flight queue. SIGINT terminates cleanly.`,
		Example: `  rex schedule run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(commandContext(cmd), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			dispatch := buildScheduleDispatcher(cmd, root)
			d, err := schedule.NewDaemon(schedule.DaemonOptions{
				WorkspaceRoot: root,
				Dispatch:      dispatch,
				OnError: func(err error) {
					fmt.Fprintf(cmd.ErrOrStderr(), "schedule: %v\n", err)
				},
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "schedule daemon running; ^C to stop")
			return d.Run(ctx)
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex schedule trigger <name>", "rex schedule list")
	return cmd
}

// --- helpers ---------------------------------------------------------

func buildScaffoldRecipe(shellCmd, harness, prompt string) *specfmt.Recipe {
	switch {
	case harness != "":
		return &specfmt.Recipe{
			Kind:    specfmt.RecipeKindHarness,
			Harness: harness,
			Prompt:  prompt,
		}
	case shellCmd != "":
		argv, err := runtask.SplitShellCommand(shellCmd)
		if err != nil || len(argv) == 0 {
			argv = []string{"sh", "-c", shellCmd}
		}
		return &specfmt.Recipe{
			Kind:    specfmt.RecipeKindShell,
			Command: argv,
		}
	default:
		// Default no-op so the schedule validates; the user can
		// fill in `run:` afterwards via `rex schedule show` + edit.
		return &specfmt.Recipe{
			Kind:    specfmt.RecipeKindShell,
			Command: []string{"true"},
		}
	}
}

func summarizeTrigger(t schedule.Trigger) string {
	switch t.Kind {
	case schedule.TriggerKindCron:
		return t.Cron
	case schedule.TriggerKindFileWatch:
		return strings.Join(t.Paths, ", ")
	default:
		return ""
	}
}

func scheduleSummaryJSON(s *schedule.Schedule) map[string]any {
	out := map[string]any{
		"name":         s.Name,
		"trigger_kind": s.Trigger.Kind,
	}
	switch s.Trigger.Kind {
	case schedule.TriggerKindCron:
		out["cron"] = s.Trigger.Cron
	case schedule.TriggerKindFileWatch:
		out["paths"] = s.Trigger.Paths
	}
	if s.Run != nil {
		out["run_kind"] = s.Run.Kind
	}
	return out
}

func scheduleFullJSON(s *schedule.Schedule) map[string]any {
	out := scheduleSummaryJSON(s)
	if s.Run != nil {
		out["run"] = s.Run
	}
	return out
}

// buildScheduleDispatcher returns a schedule.Dispatcher that
// converts a Fire into the matching runtask call. Dispatches are
// workspace-serial (mu) per EXEC-CONC.1. Runs are tagged with a
// runner.RunTrigger so RunStartedEvent.Trigger records the
// originating schedule (execution.RUN.1.3).
func buildScheduleDispatcher(cmd *cobra.Command, root string) schedule.Dispatcher {
	var mu sync.Mutex
	return func(ctx context.Context, fire schedule.Fire) error {
		mu.Lock()
		defer mu.Unlock()

		signer, err := loadOrCreateDefaultSigner(cmd)
		if err != nil {
			return err
		}
		ws, err := runtask.Open(root, runtask.WithSigner(signer))
		if err != nil {
			return err
		}
		defer ws.Close()

		trig := &runner.RunTrigger{
			Kind:     string(fire.Schedule.Trigger.Kind),
			Schedule: fire.Schedule.Name,
			Reason:   fire.Reason,
		}

		recipe := fire.Schedule.Run
		switch recipe.Kind {
		case specfmt.RecipeKindShell:
			_, err := runtask.StartShellRun(ctx, ws, runtask.ShellRunRequest{
				Command:  recipe.Command,
				Dir:      recipeDir(root, recipe.Cwd),
				Env:      recipe.Env,
				NodeID:   "shell",
				Trigger:  trig,
				WorkType: runner.WorkTypeScheduled,
			})
			return err
		case specfmt.RecipeKindHarness:
			_, err := runtask.StartHarnessRun(ctx, ws, runtask.HarnessRunRequest{
				Harness:  recipe.Harness,
				Prompt:   recipe.Prompt,
				NodeID:   "harness",
				Trigger:  trig,
				WorkType: runner.WorkTypeScheduled,
			})
			return err
		case specfmt.RecipeKindSpecValidate:
			// No primitive shipped yet; record a clear error so the
			// user understands the gap rather than silently no-op.
			return fmt.Errorf("schedule %q: run.kind=spec_validate is not yet supported by the daemon", fire.Schedule.Name)
		default:
			return fmt.Errorf("schedule %q: unknown run.kind %q", fire.Schedule.Name, recipe.Kind)
		}
	}
}

// recipeDir resolves a recipe's Cwd against the workspace root.
// Empty Cwd means the workspace root itself.
func recipeDir(root, cwd string) string {
	if cwd == "" {
		return root
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Join(root, cwd)
}
