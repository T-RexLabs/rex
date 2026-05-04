package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/hooks"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/primshell"
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/storage/eventlog"
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
	cmd.AddCommand(newRunListCmd())
	cmd.AddCommand(newRunShowCmd())
	return cmd
}

// eventLogPath returns the canonical events.log path for a workspace.
func eventLogPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, metaDirName, "events.log")
}

// logSink adapts an eventlog.Writer to the runner.EventSink interface.
type logSink struct {
	w *eventlog.Writer
}

func (s *logSink) Append(eventType string, version uint32, payload json.RawMessage) error {
	_, err := s.w.Append(eventType, version, payload)
	return err
}

// newWorkspaceWriter builds an eventlog.Writer rooted at workspace
// root, stamping the workspace's own id as the WorkspaceID, and
// composing an OnAppend that fans events out to the hooks
// dispatcher and the search indexer. The returned cleanup must be
// called by the caller (typically via defer) to drain hook workers
// and close the index handle.
func newWorkspaceWriter(workspaceRoot string) (*eventlog.Writer, *eventlog.Clock, func(), error) {
	settings, err := readWorkspaceSettings(workspaceRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	clock := eventlog.NewClock()

	global, _ := globalHooksDir()
	disp := hooks.New(hooks.Options{
		WorkspaceRoot:  workspaceRoot,
		GlobalHooksDir: global,
	})

	idx, idxErr := search.Open(workspaceRoot)
	indexerCB := search.EventIndexer(idx, nil)

	onAppend := func(rec eventlog.Record) {
		disp.OnAppend(rec)
		indexerCB(rec)
	}

	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        eventLogPath(workspaceRoot),
		WorkspaceID: settings.ID,
		Clock:       clock,
		OnAppend:    onAppend,
	})
	if err != nil {
		disp.Drain()
		if idx != nil {
			_ = idx.Close()
		}
		return nil, nil, nil, err
	}

	cleanup := func() {
		disp.Drain()
		if idx != nil {
			_ = idx.Close()
		}
	}
	if idxErr != nil {
		// Surface the open failure but proceed without
		// indexing; the user can `rex workspace reindex`
		// afterwards.
		_ = idxErr
	}
	return w, clock, cleanup, nil
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
		nodeID        string
		runIDFlag     string
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a run",
		Long: `Start a one-node DAG containing a shell_exec primitive (--shell). The
run executes synchronously in the foreground; events stream to
.rex/events.log and a final status is reported on stdout.

Harness-driven runs (--harness, --prompt, --spec from cli.RUN.1) are
deferred until execution.harness-adapter-registry lands.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shellCommand == "" {
				return errors.New("--shell is required (harness-driven runs land in a follow-up)")
			}
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root == "" {
				return errNoWorkspace
			}

			argv, err := splitShellCommand(shellCommand)
			if err != nil {
				return err
			}
			if len(argv) == 0 {
				return errors.New("--shell is empty")
			}

			cfg, err := json.Marshal(primshell.Config{Command: argv})
			if err != nil {
				return fmt.Errorf("marshal shell config: %w", err)
			}
			dag := runner.DAG{
				Nodes: []runner.Node{
					{ID: runner.NodeID(nodeID), Type: primshell.PrimitiveType, Config: cfg},
				},
			}

			writer, clock, cleanup, err := newWorkspaceWriter(root)
			if err != nil {
				return err
			}
			defer writer.Close()
			defer cleanup()

			runID := runIDFlag
			if runID == "" {
				runID = clock.Now().String()
			}

			reg := runner.NewPrimitiveRegistry()
			reg.Register(primshell.PrimitiveType, primshell.New(primshell.Options{WorkspaceDir: root}))

			exec, err := runner.NewExecutor(runner.ExecConfig{
				RunID:    runID,
				DAG:      dag,
				Sink:     &logSink{w: writer},
				Registry: reg,
			})
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			state, err := exec.Run(ctx)
			if err != nil {
				return err
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"run_id": runID,
					"status": string(state.Status),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %s: %s\n", runID, state.Status)
			node := state.Nodes[runner.NodeID(nodeID)]
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
			if state.Status != runner.RunStatusCompleted {
				return fmt.Errorf("run %s ended in status %s", runID, state.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().StringVar(&shellCommand, "shell", "", "shell command to execute as the only DAG node")
	cmd.Flags().StringVar(&nodeID, "node-id", "shell", "id assigned to the shell node in the DAG")
	cmd.Flags().StringVar(&runIDFlag, "run-id", "", "explicit run id (default: HLC-derived; useful for tests)")
	return cmd
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
			fmt.Fprintln(tw, "RUN_ID\tSTATUS\tSTARTED\tDURATION\tNODES")
			for _, s := range summaries {
				dur := "—"
				if !s.EndedAt.IsZero() {
					dur = s.EndedAt.Sub(s.StartedAt).Truncate(time.Millisecond).String()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
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

// resolveRunID accepts either a full HLC run id or a unique prefix
// of one and returns the canonical id. Run IDs are 22+ characters of
// HLC, which makes copy-paste the only realistic interactive flow;
// accepting prefixes (git-style) is the obvious ergonomic. Errors
// when the prefix matches no run or matches more than one (the
// latter lists the candidates so the user can disambiguate).
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

