package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// newLogCmd returns the `rex log` parent and wires its leaves.
//
// Bare `rex log` runs the `tail` body with default flags so the
// most common ergonomic ("just show me the recent events") works
// without typing the subcommand. `rex log tail` keeps the full
// flag surface for explicit invocation; existing scripts are
// unaffected.
func newLogCmd() *cobra.Command {
	tail := newLogTailCmd()
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Query the workspace's audit log",
		Long: `Reads .rex/events.log and presents recent or filtered audit
entries (audit.QUERY.1). Full-text search across the log lands when
the FTS index does (audit.QUERY.2).

With no subcommand, behaves like ` + "`rex log tail`" + ` with
default flags. Pass --help on tail to see filter options.`,
		Example: `  rex log
  rex log --workspace /path/to/ws
  rex log tail --workspace /path/to/ws --type run.completed`,
		RunE: tail.RunE,
	}
	setRelated(cmd,
		"rex log tail",
		"rex search <query>",
		"rex status",
	)
	addWorkspacePersistentFlag(cmd)
	cmd.AddCommand(tail)
	cmd.AddCommand(newLogSearchCmd())
	return cmd
}

func newLogSearchCmd() *cobra.Command {
	var (
		limit     int
		auditOnly bool
		typeFlag  string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search the audit log via the FTS5 index",
		Long: `Runs an FTS5 query against .rex/index.sqlite (audit.QUERY.2). By
default only audit-class event types are searched; pass
--audit-only=false to widen the scope to every indexed event.
--type narrows further to one specific type.

The query syntax accepts FTS5 features (phrase, prefix, AND/OR/NOT)
plus column qualifiers like ` + "`type:run.completed`" + `. Special
characters in tokens (hyphen, colon, dot, comma) are auto-quoted so
typing kebab-case ids works without escaping.`,
		Example: `  rex log search "permission denied"
  rex log search hello --type run.started
  rex log search workspace.created --audit-only=false`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			idx, err := search.Open(root)
			if err != nil {
				return err
			}
			defer idx.Close()

			opts := search.SearchEventsOptions{Limit: limit}
			switch {
			case typeFlag != "":
				opts.Types = []string{typeFlag}
			case auditOnly:
				opts.Types = audit.EventTypes()
			}

			results, err := idx.SearchEvents(query, opts)
			if err != nil {
				return err
			}

			if jsonOutput(cmd) {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, r := range results {
					if err := enc.Encode(r); err != nil {
						return err
					}
				}
				return nil
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no matches")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TYPE\tEVENT_ID\tSNIPPET")
			for _, r := range results {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Type, hlcShort(r.EventID), r.Snippet)
			}
			return tw.Flush()
		},
	}
	setRelated(cmd,
		"rex log tail",
		"rex search <query>",
		"rex run list",
	)
	cmd.Flags().IntVar(&limit, "limit", 25, "maximum number of matches to return")
	cmd.Flags().BoolVar(&auditOnly, "audit-only", true, "restrict to audit-class event types")
	cmd.Flags().StringVar(&typeFlag, "type", "", "restrict to one specific event type (overrides --audit-only)")
	return cmd
}

func newLogTailCmd() *cobra.Command {
	var (
		count     int
		sinceFlag string
		typeFlag  string
		actorFlag string
		auditOnly bool
	)
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Show recent audit entries (default: last 50 audit-class events)",
		Long: `Reads .rex/events.log in order and prints the last N entries
that match the supplied filters. By default only audit-class event
			types from internal/core/audit are shown; pass --audit-only=false
			to surface every record (including any non-audit-class types a
			future producer may write).

--since accepts either an RFC3339 timestamp ("2026-05-04T10:00:00Z")
or a Go duration ("1h", "24h", "30m") interpreted as "ago".

			--type, --actor, and --workspace match the corresponding record
			field exactly.`,
		Example: `  rex log tail
  rex log tail --workspace /path/to/ws --type run.completed --since 24h
  rex log tail --workspace /path/to/ws --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			filter, err := buildLogFilter(sinceFlag, typeFlag, actorFlag, auditOnly)
			if err != nil {
				return err
			}
			records, err := readAndFilter(filepath.Join(root, metaDirName, "events.log"), filter, count)
			if err != nil {
				return err
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

			if len(records) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no entries match")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TIMESTAMP\tTYPE\tACTOR\tWORKSPACE\tID")
			for _, rec := range records {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					hlcShort(rec.Timestamp.String()),
					rec.Type,
					emptyDash(rec.Actor),
					rec.WorkspaceID,
					hlcShort(rec.ID),
				)
			}
			return tw.Flush()
		},
	}
	setRelated(cmd,
		"rex search <query>",
		"rex status",
		"rex run list",
	)
	cmd.Flags().IntVarP(&count, "n", "n", 50, "number of records to show (most recent N)")
	cmd.Flags().StringVar(&sinceFlag, "since", "", "show records since this RFC3339 time or duration ago (e.g. 1h, 24h)")
	cmd.Flags().StringVar(&typeFlag, "type", "", "show only records of this event type")
	cmd.Flags().StringVar(&actorFlag, "actor", "", "show only records signed by this actor (e.g. l-<fingerprint>)")
	cmd.Flags().BoolVar(&auditOnly, "audit-only", true, "limit output to event types in the audit registry")
	return cmd
}

type logFilter struct {
	sinceTime *time.Time
	typeName  string
	actor     string
	auditOnly bool
}

func buildLogFilter(sinceFlag, typeFlag, actorFlag string, auditOnly bool) (logFilter, error) {
	f := logFilter{
		typeName:  typeFlag,
		actor:     actorFlag,
		auditOnly: auditOnly,
	}
	if sinceFlag == "" {
		return f, nil
	}
	// Try absolute first.
	if t, err := time.Parse(time.RFC3339, sinceFlag); err == nil {
		f.sinceTime = &t
		return f, nil
	}
	// Then duration-as-ago.
	if d, err := time.ParseDuration(sinceFlag); err == nil {
		t := time.Now().Add(-d)
		f.sinceTime = &t
		return f, nil
	}
	return logFilter{}, fmt.Errorf("--since %q is neither RFC3339 nor a Go duration", sinceFlag)
}

func (f logFilter) match(rec eventlog.Record) bool {
	if f.auditOnly && !audit.IsAuditEvent(rec.Type) {
		return false
	}
	if f.typeName != "" && rec.Type != f.typeName {
		return false
	}
	if f.actor != "" && rec.Actor != f.actor {
		return false
	}
	if f.sinceTime != nil {
		// HLC.Wall is unix nanoseconds; compare directly.
		if rec.Timestamp.Wall < f.sinceTime.UnixNano() {
			return false
		}
	}
	return true
}

// readAndFilter scans the log and returns up to count matching
// records, ordered most-recent-last (i.e. the natural append order).
// Holding the last N matches in a ring keeps memory bounded for
// long logs.
func readAndFilter(path string, filter logFilter, count int) ([]eventlog.Record, error) {
	r, err := eventlog.OpenReader(path)
	if err != nil {
		// Pre-first-run state: no log yet means no entries.
		if errors.Is(err, eventlog.ErrCorruptRecord) {
			return nil, err
		}
		// Fallthrough — eventlog.OpenReader returns the underlying
		// open error directly, which we surface as nil-and-empty
		// when the file is missing so the CLI prints "no entries"
		// rather than a stack-shaped error.
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()

	if count <= 0 {
		count = 50
	}
	ring := make([]eventlog.Record, 0, count)
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !filter.match(rec) {
			continue
		}
		if len(ring) < count {
			ring = append(ring, rec)
		} else {
			// Drop the oldest match; ring stays at len=count.
			ring = append(ring[1:], rec)
		}
	}
	return ring, nil
}

// hlcShort renders an HLC string compactly. The full form is
// "<unix-nanos>.<logical>" which is long; for the table view we keep
// it as-is to preserve sortability.
func hlcShort(s string) string {
	return s
}

// isNotExist reports whether err carries a "file does not exist"
// signal. eventlog.OpenReader wraps the underlying os error so we
// match on the message; refactoring the wrap shape is a separate
// concern.
func isNotExist(err error) bool {
	return strings.Contains(err.Error(), "no such file or directory") ||
		strings.Contains(err.Error(), "cannot find the file") // windows
}
