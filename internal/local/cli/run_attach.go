package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// runWatchPollInterval is the file-tail cadence for attach + the
// post-run drain in attached mode. Mirrors the web SSE handler so
// the two surfaces feel the same.
const runWatchPollInterval = 100 * time.Millisecond

func newRunAttachCmd() *cobra.Command {
	var (
		jsonOutFlag bool
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
		Example: `  rex run attach curious-giraffe
  rex run --workspace /path/to/ws attach curious-giraffe --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			runID, err := resolveRunID(root, args[0])
			if err != nil {
				return err
			}
			ctx := commandContext(cmd)
			return tailRunEvents(ctx, cmd, root, runID, jsonOutFlag)
		},
	}
	setRelated(cmd,
		"rex run show <run-id>",
		"rex run list",
		"rex run start --shell \"echo hello\"",
	)
	cmd.Flags().BoolVar(&jsonOutFlag, "json", false, "emit one decoded event record per line as JSON")
	cmd.Flags().Bool("debug", false, "include full event payloads under each line")
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
	setRelated(cmd,
		"rex run attach <run-id>",
		"rex run show <run-id>",
	)
	cmd.Flags().AddFlagSet(inner.Flags())
	return cmd
}

// tailRunEvents drives the watch loop: scan-to-EOF, then poll for
// new appends until a terminal event for runID lands or ctx fires.
func tailRunEvents(ctx context.Context, cmd *cobra.Command, root, runID string, jsonOut bool) error {
	logPath := eventLogPath(root)
	reg := runDecoderRegistry()
	out := cmd.OutOrStdout()
	debug, _ := cmd.Flags().GetBool("debug")

	seen := make(map[string]struct{})
	terminal := false

	// Default-mode emitter coalesces consecutive agent_message_chunk
	// frames; --json emits one record per line; --debug shows full
	// payloads under each summary line.
	streamEmit := newStreamPrinter(out, debug)
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
		streamEmit(rec)
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
