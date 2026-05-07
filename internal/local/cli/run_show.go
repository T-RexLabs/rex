package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// runRecord is the on-wire shape `rex run show` emits — the eventlog
// Record's structural fields plus the decoded run id when available.
type runRecord struct {
	ID          string          `json:"id"`
	Timestamp   eventlog.HLC    `json:"timestamp"`
	Type        string          `json:"type"`
	Version     uint32          `json:"version"`
	Actor       string          `json:"actor,omitempty"`
	WorkspaceID string          `json:"workspace_id"`
	Payload     json.RawMessage `json:"payload"`
}

func newRunShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show the events for one run",
		Long: `Loads all event-log records for one run and prints either a decoded
JSON stream or a human-readable transcript of the raw payloads.`,
		Example: `  rex run show curious-giraffe
  rex run --workspace /path/to/ws show curious-giraffe --json`,
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

			records, err := loadRunEvents(root, runID)
			if err != nil {
				return err
			}
			if len(records) == 0 {
				return fmt.Errorf("no events found for run %q", runID)
			}

			if jsonOutput(cmd) {
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
	setRelated(cmd,
		"rex run attach <run-id>",
		"rex run list",
		"rex log tail --type run.completed",
	)
	return cmd
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
