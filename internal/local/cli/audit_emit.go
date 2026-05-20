package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// emitAuditEvent is the shared "open writer, fire hooks + indexer,
// append one audit-class event, close" helper. Callers pass an
// already-populated payload (including any per-event-type fields
// like workspace_id); the helper does no payload-shape introspection.
//
// Every state-changing CLI command (init / workspace archive /
// unarchive / delete, repo add / link / remove, schedule add /
// remove, spec create / edit, remote add / remove, run cancel)
// routes through this single function so the
// open-writer / fire-hooks / index / append dance lives in one
// place. The hook dispatcher returned here writes hook.completed
// audit events for every fire (audit.TYPES.1 "every hook
// invocation result").
func emitAuditEvent(cmd *cobra.Command, root, eventType string, payload any) error {
	signer, err := loadOrCreateDefaultSigner(cmd)
	if err != nil {
		return err
	}

	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}

	disp := newAuditingHookDispatcher(cmd, root)
	defer disp.Drain()

	searchIdx, idxErr := search.Open(root)
	if idxErr == nil {
		defer searchIdx.Close()
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: search index unavailable: %v\n", idxErr)
	}
	indexerCB := search.EventIndexer(searchIdx, func(err error) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: index event: %v\n", err)
	})
	onAppend := func(rec eventlog.Record) {
		disp.OnAppend(rec)
		indexerCB(rec)
	}

	writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        eventLogPath(root),
		WorkspaceID: settings.ID,
		Actor:       signer.Actor().String(),
		Sign:        identity.SignFunc(signer),
		OnAppend:    onAppend,
	})
	if err != nil {
		return fmt.Errorf("open events.log: %w", err)
	}
	defer writer.Close()

	if _, err := audit.NewAppender(writer).Append(eventType, payload); err != nil {
		return fmt.Errorf("emit %s: %w", eventType, err)
	}
	return nil
}

// workspaceID returns the workspace.yaml's id; helper around
// readWorkspaceSettings so callers don't repeat the open/close
// dance just to stamp a payload.
func workspaceID(root string) (string, error) {
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return "", err
	}
	return settings.ID, nil
}
