package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/hooks"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// newAuditingHookDispatcher returns a hooks.Dispatcher whose
// Logger writes a hook.completed audit event for every hook
// result. Callers (init, repo, schedule, workspace-transition,
// emitAuditEvent, etc.) get a single construction point so
// audit.TYPES.1's "every hook invocation result" requirement
// holds without each callsite re-deriving the wiring.
//
// The Logger guards against a re-entrant write loop: any hook
// result whose triggering event was itself a hook.completed is
// silently dropped. Without the guard, a wildcard hook would
// recursively fire for its own completion event.
//
// Logger swallows write errors after surfacing them on stderr —
// the dispatcher fires asynchronously and shouldn't take down
// the parent command on a transient sqlite hiccup.
func newAuditingHookDispatcher(cmd *cobra.Command, root string) *hooks.Dispatcher {
	global, _ := globalHooksDir()
	settings, settingsErr := readWorkspaceSettings(root)
	signer, signerErr := loadOrCreateDefaultSigner(cmd)

	logger := func(res hooks.Result) {
		// Skip our own completion events to break recursion when
		// a wildcard hook is installed.
		if res.EventID == "" || isHookCompletedEventID(root, res.EventID) {
			return
		}
		if settingsErr != nil || signerErr != nil {
			// Best-effort: without settings + signer we can't
			// stamp the event correctly. Drop silently —
			// this only happens on workspaces with corrupt
			// metadata, which the user will notice elsewhere.
			return
		}
		writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
			Path:        eventLogPath(root),
			WorkspaceID: settings.ID,
			Actor:       signer.Actor().String(),
			Sign:        identity.SignFunc(signer),
		})
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: hook.completed write open: %v\n", err)
			return
		}
		defer writer.Close()
		payload := audit.HookCompletedEvent{
			WorkspaceID:    settings.ID,
			HookName:       res.HookName,
			HookScope:      res.Scope,
			HookPath:       res.Path,
			TriggerEventID: res.EventID,
			ExitCode:       res.ExitCode,
			Skipped:        res.Skipped,
			Reason:         res.Reason,
			DurationMs:     res.Duration.Milliseconds(),
		}
		if _, err := writeAudit(writer, audit.EventTypeHookCompleted, payload); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: hook.completed append: %v\n", err)
		}
	}

	return hooks.New(hooks.Options{
		WorkspaceRoot:  root,
		GlobalHooksDir: global,
		Logger:         logger,
	})
}

// writeAudit is a small wrapper around audit.NewAppender(...).Append
// so the hook-completed Logger doesn't have to thread the audit
// package twice through itself. Returns the appended record so
// future call paths can chain on it.
func writeAudit(w *eventlog.Writer, eventType string, payload any) (eventlog.Record, error) {
	return audit.NewAppender(w).Append(eventType, payload)
}

// isHookCompletedEventID reports whether the given trigger event
// id is itself a hook.completed event in this workspace's log. We
// look it up rather than relying on a name suffix because eventlog
// IDs are HLC-derived and don't encode their type. Cheap because
// the workspace's events.log is small in v1; a future watcher
// could cache this if it shows up on profiles.
func isHookCompletedEventID(root, eventID string) bool {
	r, err := eventlog.OpenReader(eventLogPath(root))
	if err != nil {
		return false
	}
	defer r.Close()
	for {
		rec, err := r.Next()
		if err != nil {
			return false
		}
		if rec.ID == eventID {
			return rec.Type == audit.EventTypeHookCompleted
		}
	}
}
