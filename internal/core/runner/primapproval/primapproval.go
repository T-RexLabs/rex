// Package primapproval implements the human_approval primitive
// defined in execution.PRIM.4: pause the run until a user
// approves or denies via CLI/UI; output is the decision and the
// approver's identity.
//
// The primitive bridges two existing event types — already used
// by the harness's permission flow — into a standalone DAG node:
//
//  1. emit PermissionRequestedEvent through the Options.Sink
//  2. tail events.log for a matching PermissionGrantedEvent or
//     PermissionDeniedEvent (request_id match)
//  3. return Output{decision, approver, note} on grant; return an
//     error so the run aborts on deny (consistent with PRIM.4's
//     "until a user approves or denies — output is the decision
//     and the approver's identity")
//
// Resolution events come from a separate process: typically
// `rex run approve <run-id>` or `rex run deny <run-id>` (cli.RUN.7),
// or the web UI's permission card. v1 ships the file-tail + CLI
// path; the web UI for human_approval lands when the executor's
// permission router gains a parallel surface.
package primapproval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// PrimitiveType is the canonical Node.Type string.
const PrimitiveType = "human_approval"

// DefaultPollInterval is the file-tail cadence the primitive uses
// to look for resolution events. Mirrors the cancel watcher and
// web SSE pollers so latency feels consistent.
const DefaultPollInterval = 100 * time.Millisecond

// Config is the JSON shape stored in Node.Config.
type Config struct {
	// Tool is a free-form label the requester wants the
	// approver to see. Often the action being authorised
	// ("delete-prod", "publish-release"). Optional.
	Tool string `json:"tool,omitempty"`
	// Reason is the longer-form explanation of why approval
	// is being asked. Optional.
	Reason string `json:"reason,omitempty"`
	// Args is opaque structured context the approver may
	// inspect ("the rows about to be touched", etc.).
	// Optional; passed through verbatim.
	Args json.RawMessage `json:"args,omitempty"`
	// Timeout caps how long the primitive blocks on the
	// resolution. Zero = no timeout. Distinct from PERM.3's
	// per-workspace policy because primitive-level timeouts
	// are easier to test deterministically.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Decision is the outcome the primitive emits in its Output.
type Decision string

const (
	DecisionGranted Decision = "granted"
	DecisionDenied  Decision = "denied"
)

// Output is what the primitive returns. On grant the run proceeds
// with the output; on deny the primitive returns an error and the
// executor aborts the run, but Output is also marshalled into the
// node-failed event so audit readers see who denied.
type Output struct {
	RequestID string   `json:"request_id"`
	Decision  Decision `json:"decision"`
	Approver  string   `json:"approver,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Options configure New. WorkspaceRoot + Sink are required;
// PollInterval and Now default when zero.
type Options struct {
	// WorkspaceRoot is the absolute workspace path. The primitive
	// derives the events.log location for resolution polling.
	WorkspaceRoot string
	// LogPath is the explicit events.log location. When empty,
	// derived from WorkspaceRoot. Tests typically set this
	// directly to a temp file.
	LogPath string
	// Sink is how the primitive emits PermissionRequestedEvent.
	// Required — without it the request never lands so no
	// approver could see it.
	Sink runner.EventSink
	// PollInterval overrides DefaultPollInterval.
	PollInterval time.Duration
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// New returns the primitive bound to opts.
func New(opts Options) runner.Primitive {
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return runner.PrimitiveFunc(func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		if opts.Sink == nil {
			return runner.PrimitiveOutput{}, errors.New("primapproval: Sink is required")
		}
		logPath := opts.LogPath
		if logPath == "" {
			if opts.WorkspaceRoot == "" {
				return runner.PrimitiveOutput{}, errors.New("primapproval: WorkspaceRoot or LogPath is required")
			}
			logPath = fmt.Sprintf("%s/.rex/events.log", strings.TrimRight(opts.WorkspaceRoot, "/"))
		}

		var cfg Config
		if len(in.Node.Config) > 0 {
			if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
				return runner.PrimitiveOutput{}, fmt.Errorf("primapproval: decode config: %w", err)
			}
		}

		requestID := fmt.Sprintf("%s.approval.%s", in.RunID, in.Node.ID)
		req := runner.PermissionRequestedEvent{
			RunID:       in.RunID,
			NodeID:      in.Node.ID,
			RequestID:   requestID,
			Tool:        cfg.Tool,
			Args:        cfg.Args,
			Reason:      cfg.Reason,
			RequestedAt: now(),
		}
		body, err := json.Marshal(req)
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primapproval: marshal request: %w", err)
		}
		if err := opts.Sink.Append(runner.EventTypePermissionRequested, runner.EventVersion, body); err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primapproval: emit request: %w", err)
		}

		// Wait for resolution. Poll loop mirrors the cancel
		// watcher; the resolution event comes from a separate
		// process (rex run approve/deny) or the web UI.
		waitCtx := ctx
		if cfg.Timeout > 0 {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
			defer cancel()
		}
		decision, approver, note, derr := waitForResolution(waitCtx, logPath, requestID, pollInterval)
		if derr != nil {
			return runner.PrimitiveOutput{}, derr
		}
		out := Output{RequestID: requestID, Decision: decision, Approver: approver, Note: note}
		outBody, err := json.Marshal(out)
		if err != nil {
			return runner.PrimitiveOutput{}, fmt.Errorf("primapproval: marshal output: %w", err)
		}
		if decision == DecisionDenied {
			return runner.PrimitiveOutput{Output: outBody}, fmt.Errorf("primapproval: denied by %s: %s", approver, note)
		}
		return runner.PrimitiveOutput{Output: outBody}, nil
	})
}

// waitForResolution tails logPath for PermissionGranted /
// PermissionDenied events whose RequestID matches the one we
// emitted. Returns on first match, on ctx cancel, or on log read
// error.
func waitForResolution(ctx context.Context, logPath, requestID string, poll time.Duration) (Decision, string, string, error) {
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)
	seen := make(map[string]struct{})

	scan := func() (Decision, string, string, bool, error) {
		r, err := eventlog.OpenReader(logPath)
		if err != nil {
			return "", "", "", false, nil
		}
		defer r.Close()
		for {
			rec, err := r.Next()
			if errors.Is(err, io.EOF) {
				return "", "", "", false, nil
			}
			if err != nil {
				return "", "", "", false, err
			}
			if rec.Type != runner.EventTypePermissionGranted && rec.Type != runner.EventTypePermissionDenied {
				continue
			}
			if _, dup := seen[rec.ID]; dup {
				continue
			}
			seen[rec.ID] = struct{}{}
			decoded, derr := reg.Decode(event.Envelope{Type: rec.Type, Version: rec.Version, Payload: rec.Payload})
			if derr != nil {
				continue
			}
			switch ev := decoded.(type) {
			case runner.PermissionGrantedEvent:
				if ev.RequestID == requestID {
					return DecisionGranted, ev.Approver, ev.Note, true, nil
				}
			case runner.PermissionDeniedEvent:
				if ev.RequestID == requestID {
					return DecisionDenied, ev.Approver, ev.Reason, true, nil
				}
			}
		}
	}

	if d, a, n, ok, err := scan(); err != nil {
		return "", "", "", err
	} else if ok {
		return d, a, n, nil
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", "", "", ctx.Err()
		case <-ticker.C:
			if d, a, n, ok, err := scan(); err != nil {
				return "", "", "", err
			} else if ok {
				return d, a, n, nil
			}
		}
	}
}
