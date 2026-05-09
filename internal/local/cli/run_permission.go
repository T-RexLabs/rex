// Package cli — `rex run approve` / `rex run deny` (cli.RUN.7).
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// newRunApproveCmd implements `rex run approve <run-id>` per
// cli.RUN.7. Resolves a pending permission prompt for the named
// run by writing a PermissionGrantedEvent that the running
// process's primapproval primitive picks up via its tail loop.
//
// When --request-id is omitted the command picks the
// most-recently-requested unresolved permission for the run.
func newRunApproveCmd() *cobra.Command {
	return newRunPermissionResolutionCmd("approve", "Approve a pending permission prompt", true)
}

// newRunDenyCmd implements `rex run deny <run-id>` per cli.RUN.7.
// Same shape as approve but writes PermissionDeniedEvent.
func newRunDenyCmd() *cobra.Command {
	return newRunPermissionResolutionCmd("deny", "Deny a pending permission prompt", false)
}

// newRunPermissionResolutionCmd is the shared body of approve and
// deny — they differ only in the event type written. Keeps the two
// commands in lockstep on flag set + resolution logic.
func newRunPermissionResolutionCmd(verb, short string, granted bool) *cobra.Command {
	var (
		requestID string
		note      string
	)
	cmd := &cobra.Command{
		Use:   verb + " <run-id>",
		Short: short,
		Long:  longResolutionHelp(verb, granted),
		Example: fmt.Sprintf(`  rex run %s curious-giraffe
  rex run %s curious-giraffe --request-id <id>
  rex run %s curious-giraffe --note "looks fine"`, verb, verb, verb),
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

			signer, err := loadOrCreateDefaultSigner(cmd)
			if err != nil {
				return err
			}
			settings, err := readWorkspaceSettings(root)
			if err != nil {
				return err
			}

			rid, nodeID, err := resolveRequestID(root, runID, requestID)
			if err != nil {
				return err
			}

			writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
				Path:        eventLogPath(root),
				WorkspaceID: settings.ID,
				Actor:       signer.Actor().String(),
				Sign:        identity.SignFunc(signer),
			})
			if err != nil {
				return fmt.Errorf("open events.log: %w", err)
			}
			defer writer.Close()

			actor := signer.Actor().String()
			now := time.Now().UTC()

			var (
				eventType string
				body      []byte
			)
			if granted {
				eventType = runner.EventTypePermissionGranted
				body, err = json.Marshal(runner.PermissionGrantedEvent{
					RunID:     runID,
					NodeID:    nodeID,
					RequestID: rid,
					Approver:  actor,
					GrantedAt: now,
					Note:      note,
				})
			} else {
				eventType = runner.EventTypePermissionDenied
				body, err = json.Marshal(runner.PermissionDeniedEvent{
					RunID:     runID,
					NodeID:    nodeID,
					RequestID: rid,
					Approver:  actor,
					DeniedAt:  now,
					Reason:    note,
				})
			}
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			if _, err := writer.Append(eventType, runner.EventVersion, body); err != nil {
				return fmt.Errorf("append: %w", err)
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"run_id":     runID,
					"request_id": rid,
					"node_id":    string(nodeID),
					"decision":   verb,
					"actor":      actor,
				})
			}
			past := "approved"
			if !granted {
				past = "denied"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s request %s for %s\n",
				past, rid, runner.FriendlyName(runID))
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.Flags().StringVar(&requestID, "request-id", "", "specific permission request id (default: most recent unresolved)")
	cmd.Flags().StringVar(&note, "note", "", "free-form note recorded with the decision")
	setRelated(cmd, "rex run show <run-id>", "rex run attach <run-id>", "rex run cancel <run-id>")
	return cmd
}

func longResolutionHelp(verb string, granted bool) string {
	out := "Resolves a pending permission prompt for the named run by writing\n"
	if granted {
		out += "a PermissionGranted event"
	} else {
		out += "a PermissionDenied event"
	}
	out += " that the running process's human_approval\nprimitive (or harness ACP permission flow) picks up on its watcher\ntick (~100ms). The executor then "
	if granted {
		out += "proceeds with the run."
	} else {
		out += "fails the node."
	}
	out += "\n\nWith --request-id, resolves a specific request. Without it, picks\nthe most-recently-requested unresolved permission for the run."
	return out
}

// resolveRequestID returns (request_id, node_id) for the prompt
// the user wants to resolve. With explicit requestIDFlag, looks
// it up. Without, walks the events.log for the run and finds the
// most-recent PermissionRequested event without a matching
// Granted/Denied resolution.
func resolveRequestID(root, runID, requestIDFlag string) (string, runner.NodeID, error) {
	r, err := eventlog.OpenReader(eventLogPath(root))
	if err != nil {
		return "", "", fmt.Errorf("open events.log: %w", err)
	}
	defer r.Close()

	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	type pending struct {
		id     string
		nodeID runner.NodeID
		seq    int
	}
	pendingByID := map[string]pending{}
	resolved := map[string]struct{}{}
	seq := 0
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", err
		}
		seq++
		if rec.Type != runner.EventTypePermissionRequested &&
			rec.Type != runner.EventTypePermissionGranted &&
			rec.Type != runner.EventTypePermissionDenied {
			continue
		}
		decoded, derr := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if derr != nil {
			continue
		}
		switch ev := decoded.(type) {
		case runner.PermissionRequestedEvent:
			if ev.RunID != runID {
				continue
			}
			pendingByID[ev.RequestID] = pending{id: ev.RequestID, nodeID: ev.NodeID, seq: seq}
		case runner.PermissionGrantedEvent:
			if ev.RunID == runID {
				resolved[ev.RequestID] = struct{}{}
			}
		case runner.PermissionDeniedEvent:
			if ev.RunID == runID {
				resolved[ev.RequestID] = struct{}{}
			}
		}
	}

	// Drop already-resolved entries.
	for id := range resolved {
		delete(pendingByID, id)
	}

	if requestIDFlag != "" {
		p, ok := pendingByID[requestIDFlag]
		if !ok {
			return "", "", fmt.Errorf("no unresolved permission request %q for run %q", requestIDFlag, runID)
		}
		return p.id, p.nodeID, nil
	}

	if len(pendingByID) == 0 {
		return "", "", fmt.Errorf("run %q has no unresolved permission requests", runID)
	}

	// Pick the most-recent entry (highest seq).
	var newest pending
	for _, p := range pendingByID {
		if p.seq > newest.seq {
			newest = p
		}
	}
	return newest.id, newest.nodeID, nil
}
