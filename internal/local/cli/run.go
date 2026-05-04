package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/core/runner/primharness"
	"github.com/asabla/rex/internal/core/runner/primshell"
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
	}
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
// decode every runner event type. Used by list and show to read the
// log without each command rebuilding the table.
func runDecoderRegistry() *event.Registry {
	r := event.NewRegistry()
	runner.RegisterEvents(r)
	return r
}

func newRunStartCmd() *cobra.Command {
	var (
		workspaceFlag string
		shellCommand  string
		harnessFlag   string
		promptFlag    string
		modelFlag     string
		modeFlag      string
		timeoutFlag   time.Duration
		nodeID        string
		runIDFlag     string
		quietFlag     bool
		detachFlag    bool
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
		RunE: func(cmd *cobra.Command, args []string) error {
			if detachFlag {
				return errors.New("--detach is deferred until backgrounding/daemon support lands (cli.RUN.1)")
			}
			shellMode := shellCommand != ""
			harnessMode := harnessFlag != ""
			if shellMode == harnessMode {
				return errors.New("exactly one of --shell or --harness is required")
			}
			if harnessMode && promptFlag == "" {
				return errors.New("--prompt is required with --harness")
			}
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root == "" {
				return errNoWorkspace
			}

			ws, err := runtask.Open(root)
			if err != nil {
				return err
			}
			defer ws.Close()

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			onEvent := liveEventPrinter(cmd, quietFlag)

			if harnessMode {
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
				Command: argv,
				NodeID:  nodeID,
				RunID:   runIDFlag,
				OnEvent: onEvent,
			})
			if err != nil {
				return err
			}
			return reportShellRun(cmd, res, nodeID)
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
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
// those modes.
func liveEventPrinter(cmd *cobra.Command, quiet bool) func(eventlog.Record) {
	if quiet {
		return nil
	}
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return nil
	}
	out := cmd.OutOrStdout()
	return func(rec eventlog.Record) {
		fmt.Fprintf(out, "%s  %-22s  %s\n",
			formatHLCTime(rec.Timestamp), rec.Type, summarizeEventPayload(rec.Type, rec.Payload))
	}
}

// reportShellRun writes the human-friendly tail block for a shell
// run; used to keep newRunStartCmd readable now that it branches.
func reportShellRun(cmd *cobra.Command, res *runtask.ShellRunResult, nodeID string) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
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
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
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

func newRunListCmd() *cobra.Command {
	var (
		workspaceFlag string
		statusFilter  string
		limit         int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs from the workspace event log",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root == "" {
				return errNoWorkspace
			}
			summaries, err := readRunSummaries(root)
			if err != nil {
				return err
			}
			if statusFilter != "" {
				filtered := summaries[:0]
				for _, s := range summaries {
					if string(s.Status) == statusFilter {
						filtered = append(filtered, s)
					}
				}
				summaries = filtered
			}
			sort.Slice(summaries, func(i, j int) bool {
				return summaries[i].StartedAt.After(summaries[j].StartedAt)
			})
			if limit > 0 && len(summaries) > limit {
				summaries = summaries[:limit]
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, s := range summaries {
					if err := enc.Encode(s); err != nil {
						return err
					}
				}
				return nil
			}
			if len(summaries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no runs yet")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tRUN_ID\tSTATUS\tSTARTED\tDURATION\tNODES")
			for _, s := range summaries {
				dur := "—"
				if !s.EndedAt.IsZero() {
					dur = s.EndedAt.Sub(s.StartedAt).Truncate(time.Millisecond).String()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
					runner.FriendlyName(s.RunID),
					s.RunID, s.Status, s.StartedAt.UTC().Format(time.RFC3339), dur, s.NodeEvents)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().StringVar(&statusFilter, "status", "", "only show runs with the given final status")
	cmd.Flags().IntVar(&limit, "limit", 0, "show only the N most recent runs (0 = no limit)")
	return cmd
}

func newRunShowCmd() *cobra.Command {
	var workspaceFlag string
	cmd := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show the events for one run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root == "" {
				return errNoWorkspace
			}
			runID, err := resolveRunID(root, args[0])
			if err != nil {
				return err
			}

			records, err := loadRunEvents(root, runID)
			if err != nil {
				return err
			}
			if len(records) == 0 {
				return fmt.Errorf("no events found for run %q", runID)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, rec := range records {
					if err := enc.Encode(rec); err != nil {
						return err
					}
				}
				return nil
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "run %s — %d events\n\n", runID, len(records))
			for _, rec := range records {
				fmt.Fprintf(out, "%s  %s\n", rec.Timestamp.String(), rec.Type)
				if len(rec.Payload) > 0 {
					if pretty, err := json.MarshalIndent(json.RawMessage(rec.Payload), "    ", "  "); err == nil {
						fmt.Fprintf(out, "    %s\n", pretty)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	return cmd
}

// runSummary is one row reported by `rex run list`.
type runSummary struct {
	RunID      string             `json:"run_id"`
	Status     runner.RunStatus   `json:"status"`
	StartedAt  time.Time          `json:"started_at"`
	EndedAt    time.Time          `json:"ended_at,omitempty"`
	NodeEvents int                `json:"node_events"`
}

// readRunSummaries scans the workspace's events.log and aggregates one
// runSummary per distinct run_id. Runs with no terminal event surface
// as RunStatusRunning; that lets list correctly distinguish in-flight
// runs from completed ones once a daemon model exists.
func readRunSummaries(workspaceRoot string) ([]runSummary, error) {
	path := eventLogPath(workspaceRoot)
	r, err := openReaderForPath(path)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	defer r.Close()

	reg := runDecoderRegistry()
	byID := map[string]*runner.RunSummary{}
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		decoded, err := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if errors.Is(err, event.ErrSkipUnknownType) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var probe runner.RunSummary
		if !probe.FoldEvent(decoded) {
			continue
		}
		s, ok := byID[probe.RunID]
		if !ok {
			s = &runner.RunSummary{}
			byID[probe.RunID] = s
		}
		s.FoldEvent(decoded)
	}

	out := make([]runSummary, 0, len(byID))
	for _, s := range byID {
		out = append(out, runSummary{
			RunID:      s.RunID,
			Status:     s.EffectiveStatus(),
			StartedAt:  s.StartedAt,
			EndedAt:    s.EndedAt,
			NodeEvents: s.NodeEvents,
		})
	}
	return out, nil
}

// runRecord is the on-wire shape `rex run show` emits — the eventlog
// Record's structural fields plus the decoded run id when available.
type runRecord struct {
	ID          string             `json:"id"`
	Timestamp   eventlog.HLC       `json:"timestamp"`
	Type        string             `json:"type"`
	Version     uint32             `json:"version"`
	Actor       string             `json:"actor,omitempty"`
	WorkspaceID string             `json:"workspace_id"`
	Payload     json.RawMessage    `json:"payload"`
}

// loadRunEvents returns every record in events.log whose decoded
// runner event has the matching RunID. Decoding is required because
// the run id lives inside the typed payloads, not on the Record
// envelope.
func loadRunEvents(workspaceRoot, runID string) ([]runRecord, error) {
	path := eventLogPath(workspaceRoot)
	r, err := openReaderForPath(path)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	defer r.Close()

	reg := runDecoderRegistry()
	var out []runRecord
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		decoded, err := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if errors.Is(err, event.ErrSkipUnknownType) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if runner.MatchesRun(decoded, runID) {
			out = append(out, runRecord{
				ID:          rec.ID,
				Timestamp:   rec.Timestamp,
				Type:        rec.Type,
				Version:     rec.Version,
				Actor:       rec.Actor,
				WorkspaceID: rec.WorkspaceID,
				Payload:     rec.Payload,
			})
		}
	}
	return out, nil
}

// resolveRunID accepts either a full HLC run id, a unique prefix
// of one, or a friendly slug ("brave-otter") and returns the
// canonical HLC. Resolution order: exact HLC, friendly slug, HLC
// prefix. Errors when nothing matches or when a prefix matches more
// than one run (the latter lists the candidates so the user can
// disambiguate).
func resolveRunID(workspaceRoot, given string) (string, error) {
	summaries, err := readRunSummaries(workspaceRoot)
	if err != nil {
		return "", err
	}
	// Exact match wins immediately so a full id never pays the
	// scan cost of "could this be a prefix".
	for _, s := range summaries {
		if s.RunID == given {
			return given, nil
		}
	}
	// Friendly slug match: walk every run, hash its id, see if
	// it matches the input. Cheaper than O(slugs) since we only
	// hash the runs the user has, not the whole dictionary.
	if runner.IsFriendlyName(given) {
		var slugMatches []string
		for _, s := range summaries {
			if runner.FriendlyName(s.RunID) == given {
				slugMatches = append(slugMatches, s.RunID)
			}
		}
		if len(slugMatches) == 1 {
			return slugMatches[0], nil
		}
		if len(slugMatches) > 1 {
			return "", fmt.Errorf("friendly name %q is ambiguous; matches %d runs: %s",
				given, len(slugMatches), strings.Join(slugMatches, ", "))
		}
		// fall through to prefix match — a hyphenated string
		// might still be a valid HLC prefix in pathological cases
	}
	var matches []string
	for _, s := range summaries {
		if strings.HasPrefix(s.RunID, given) {
			matches = append(matches, s.RunID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no events found for run %q", given)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("run id %q is ambiguous; matches %d runs: %s",
			given, len(matches), strings.Join(matches, ", "))
	}
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

// runWatchPollInterval is the file-tail cadence for attach + the
// post-run drain in attached mode. Mirrors the web SSE handler so
// the two surfaces feel the same.
const runWatchPollInterval = 100 * time.Millisecond

func newRunAttachCmd() *cobra.Command {
	var (
		workspaceFlag string
		jsonOutFlag   bool
	)
	cmd := &cobra.Command{
		Use:   "attach <run-id>",
		Short: "Re-attach to an in-flight or completed run and stream its events",
		Long: `Re-attach to one run's event stream in the terminal. Replays every
event already in events.log for the run, then polls the file for
new appends until the run reaches a terminal state (completed /
cancelled / aborted) or the user interrupts with Ctrl-C.

Run IDs accept the same git-style prefix matching as 'rex run show'.

Output: one line per event of the form
  <timestamp>  <event-type>  <one-line summary>
With --json, one decoded event record per line.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root == "" {
				return errNoWorkspace
			}
			runID, err := resolveRunID(root, args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return tailRunEvents(ctx, cmd, root, runID, jsonOutFlag)
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().BoolVar(&jsonOutFlag, "json", false, "emit one decoded event record per line as JSON")
	return cmd
}

// newRunWatchAliasCmd is the deprecated alias for `rex run attach`,
// kept so existing scripts keep working through one release. It
// dispatches into newRunAttachCmd's RunE rather than duplicating
// the logic; cli.RUN.2 calls out the deprecation explicitly.
func newRunWatchAliasCmd() *cobra.Command {
	inner := newRunAttachCmd()
	cmd := &cobra.Command{
		Use:        "watch <run-id>",
		Short:      "Deprecated alias for `rex run attach`",
		Hidden:     true,
		Deprecated: "use `rex run attach` instead",
		Args:       cobra.ExactArgs(1),
		RunE:       inner.RunE,
	}
	cmd.Flags().AddFlagSet(inner.Flags())
	return cmd
}

// tailRunEvents drives the watch loop: scan-to-EOF, then poll for
// new appends until a terminal event for runID lands or ctx fires.
func tailRunEvents(ctx context.Context, cmd *cobra.Command, root, runID string, jsonOut bool) error {
	logPath := eventLogPath(root)
	reg := runDecoderRegistry()
	out := cmd.OutOrStdout()

	seen := make(map[string]struct{})
	terminal := false

	emit := func(rec eventlog.Record) error {
		if jsonOut {
			return json.NewEncoder(out).Encode(runRecord{
				ID:          rec.ID,
				Timestamp:   rec.Timestamp,
				Type:        rec.Type,
				Version:     rec.Version,
				Actor:       rec.Actor,
				WorkspaceID: rec.WorkspaceID,
				Payload:     rec.Payload,
			})
		}
		fmt.Fprintf(out, "%s  %-22s  %s\n",
			formatHLCTime(rec.Timestamp), rec.Type, summarizeEventPayload(rec.Type, rec.Payload))
		return nil
	}

	scan := func() error {
		f, err := openReaderForPath(logPath)
		if err != nil {
			return err
		}
		if f == nil {
			return nil
		}
		defer f.Close()
		for {
			rec, err := f.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if !recordMatchesRun(reg, rec, runID) {
				continue
			}
			if _, dup := seen[rec.ID]; dup {
				continue
			}
			seen[rec.ID] = struct{}{}
			if err := emit(rec); err != nil {
				return err
			}
			switch rec.Type {
			case runner.EventTypeRunCompleted,
				runner.EventTypeRunCancelled,
				runner.EventTypeRunAborted:
				terminal = true
				return nil
			}
		}
	}

	if err := scan(); err != nil {
		return err
	}
	if terminal {
		return nil
	}

	ticker := time.NewTicker(runWatchPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := scan(); err != nil {
				return err
			}
			if terminal {
				return nil
			}
		}
	}
}

// formatHLCTime renders the wall component of an HLC as a local-
// timezone "2006-01-02 15:04:05.000" string. Falls back to the raw
// HLC representation when the timestamp is zero so we never silently
// emit a meaningless "1970-01-01" line.
func formatHLCTime(h eventlog.HLC) string {
	if h.Wall == 0 {
		return h.String()
	}
	return h.Time().Local().Format("2006-01-02 15:04:05.000")
}

// recordMatchesRun decodes rec via the runner registry and asks
// runner.MatchesRun whether the payload references runID. Local copy
// of the web/runs.go helper to avoid importing internal/local/web
// from the CLI; the implementations are intentionally identical so
// CLI and web stay in lockstep.
func recordMatchesRun(reg *event.Registry, rec eventlog.Record, runID string) bool {
	decoded, err := reg.Decode(event.Envelope{
		Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
	})
	if err != nil {
		return false
	}
	return runner.MatchesRun(decoded, runID)
}

// summarizeEventPayload turns a runner event payload into a one-line
// summary suitable for terminal output. The cases mirror the runner
// event-type constants; unknown types fall back to a length hint.
func summarizeEventPayload(eventType string, payload json.RawMessage) string {
	switch eventType {
	case runner.EventTypeRunStarted:
		var ev runner.RunStartedEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			return fmt.Sprintf("run=%s", runner.FriendlyName(ev.RunID))
		}
	case runner.EventTypeRunCompleted:
		var ev runner.RunCompletedEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			return fmt.Sprintf("run=%s status=completed", runner.FriendlyName(ev.RunID))
		}
	case runner.EventTypeRunCancelled:
		return "status=cancelled"
	case runner.EventTypeRunAborted:
		var ev struct {
			RunID string `json:"run_id"`
			Error string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(payload, &ev); err == nil {
			if ev.Error != "" {
				return fmt.Sprintf("run=%s aborted: %s", ev.RunID, ev.Error)
			}
			return fmt.Sprintf("run=%s status=aborted", ev.RunID)
		}
	case runner.EventTypeNodeStarted, runner.EventTypeNodeSucceeded,
		runner.EventTypeNodeFailed, runner.EventTypeNodeRetried:
		var ev struct {
			NodeID string `json:"node_id"`
			Error  string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(payload, &ev); err == nil {
			if ev.Error != "" {
				return fmt.Sprintf("node=%s err=%s", ev.NodeID, ev.Error)
			}
			return fmt.Sprintf("node=%s", ev.NodeID)
		}
	case runner.EventTypePermissionRequested,
		runner.EventTypePermissionGranted,
		runner.EventTypePermissionDenied:
		var ev struct {
			NodeID    string `json:"node_id"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(payload, &ev); err == nil {
			return fmt.Sprintf("node=%s request=%s", ev.NodeID, ev.RequestID)
		}
	case runner.EventTypeHarnessFrame:
		return summarizeHarnessFrame(payload)
	}
	return fmt.Sprintf("(%d bytes)", len(payload))
}

// summarizeHarnessFrame extracts the most useful one-liner out of an
// ACP frame for the human-readable event stream. Priority:
//   1. an agent_message_chunk text — the actual model output
//   2. a tool call name — when the harness invokes a tool
//   3. the bare ACP method name as a fallback
// Length is capped so a long agent message doesn't blow out one
// terminal line; full content is in `rex run show <id>`.
func summarizeHarnessFrame(payload json.RawMessage) string {
	var ev runner.HarnessFrameEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Sprintf("(%d bytes)", len(payload))
	}
	// Try the typed update path first.
	var frame struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(ev.Frame, &frame)

	if len(frame.Params) > 0 {
		var p struct {
			Update struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
				Text    string          `json:"text,omitempty"`
				Tool    struct {
					Name string `json:"name"`
				} `json:"tool"`
			} `json:"update"`
		}
		if err := json.Unmarshal(frame.Params, &p); err == nil && p.Update.Type != "" {
			text := extractFrameText(p.Update.Type, p.Update.Text, p.Update.Content, p.Update.Tool.Name)
			if text != "" {
				return fmt.Sprintf("%s %s", p.Update.Type, truncate(text, 80))
			}
			return p.Update.Type
		}
	}
	if ev.Method != "" {
		return ev.Method
	}
	if len(frame.Result) > 0 {
		return "result"
	}
	return "(frame)"
}

// extractFrameText pulls a human-readable string out of a
// session/update payload. Different update types put the text in
// different places; this collapses them into one return.
func extractFrameText(updateType, fallbackText string, content json.RawMessage, toolName string) string {
	if fallbackText != "" {
		return fallbackText
	}
	if toolName != "" {
		return toolName
	}
	if len(content) > 0 {
		// content may be {type:"text", text:"..."} or an array of
		// such blocks. Try both shapes.
		var single struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(content, &single); err == nil && single.Text != "" {
			return single.Text
		}
		var arr []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(content, &arr); err == nil {
			for _, b := range arr {
				if b.Text != "" {
					return b.Text
				}
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// splitShellCommand parses a --shell argument into argv. For v1 we
// accept POSIX-ish quoted strings: bare words and double-quoted runs
// of arbitrary characters. Single-quoted runs and backslash escapes
// can land later if usage demands; the function rejects unbalanced
// quotes loudly so the user is not silently misinterpreted.
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

