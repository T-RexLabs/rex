package audit

import (
	"sort"

	"github.com/asabla/rex/internal/core/runner"
)

// Audit-class event type names. The list is the v1 enumeration of
// what audit.TYPES.1 calls out, restricted to entries with concrete
// producers in the current codebase. Adding a type to this registry
// is an additive change (overview.SYS.4); removing one is forbidden.
//
// Three sources contribute:
//   - workspace lifecycle events (this package)
//   - runner events (re-exported from internal/core/runner so the
//     registry has a single source of truth)
//   - future producers: spec.edit, sync.push, auth.success, etc.
const (
	// EventTypeWorkspaceCreated fires from `rex workspace init`.
	EventTypeWorkspaceCreated = "workspace.created"

	// Repo-attach lifecycle (workspace.REPO.4.1). All audit-class
	// via audit.TYPES.1's "every workspace state change" clause.
	EventTypeRepoAdded   = "repo.added"
	EventTypeRepoLinked  = "repo.linked"
	EventTypeRepoRemoved = "repo.removed"
)

// EventVersion is the schema version for audit-package event payloads.
// Bump only on a semantically-incompatible change to an existing
// shape; new fields must be additive.
const EventVersion uint32 = 1

// auditEventTypes holds the set of event-type strings that are
// audit-class. Construction time only; later reads are racy-safe
// because Go reads from a non-mutated map are safe.
var auditEventTypes = func() map[string]struct{} {
	out := map[string]struct{}{
		EventTypeWorkspaceCreated: {},
		EventTypeRepoAdded:        {},
		EventTypeRepoLinked:       {},
		EventTypeRepoRemoved:      {},

		// Runner events are audit-class per TYPES.1 ("every harness
		// invocation start/end ... every workspace state change").
		runner.EventTypeRunStarted:          {},
		runner.EventTypeRunCompleted:        {},
		runner.EventTypeRunCancelled:        {},
		runner.EventTypeRunAborted:          {},
		runner.EventTypeNodeStarted:         {},
		runner.EventTypeNodeSucceeded:       {},
		runner.EventTypeNodeFailed:          {},
		runner.EventTypeNodeRetried:         {},
		runner.EventTypePermissionRequested: {},
		runner.EventTypePermissionGranted:   {},
		runner.EventTypePermissionDenied:    {},
	}
	return out
}()

// IsAuditEvent reports whether eventType is audit-class.
func IsAuditEvent(eventType string) bool {
	_, ok := auditEventTypes[eventType]
	return ok
}

// EventTypes returns the registered audit-class event-type names in
// lex order. Useful for `rex log tail --type` autocomplete and for
// the `rex log search` event-type filter.
func EventTypes() []string {
	out := make([]string, 0, len(auditEventTypes))
	for t := range auditEventTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// WorkspaceCreatedEvent is the payload for EventTypeWorkspaceCreated.
type WorkspaceCreatedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	CreatedAt   string `json:"created_at"`
	// CreatedBy is the actor string of the identity that ran the
	// init command (`<role>-<fingerprint>` per
	// identity.Actor.String). May be empty until a default identity
	// is auto-created on workspace init; the field is the place for
	// that to land additively.
	CreatedBy string `json:"created_by,omitempty"`
}

// RepoAddedEvent is the payload for EventTypeRepoAdded — fires
// from `rex repo add` after the clone succeeds and the
// registration in workspace.yaml is persisted (workspace.REPO.4.1).
type RepoAddedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	URL         string `json:"url"`
}

// RepoLinkedEvent is the payload for EventTypeRepoLinked — fires
// from `rex repo link` (workspace.REPO.4.1). No URL field by
// design: linked repos have no rex-managed origin.
type RepoLinkedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
}

// RepoRemovedEvent is the payload for EventTypeRepoRemoved — fires
// from `rex repo remove` (workspace.REPO.4.1). Purged is true iff
// the working copy was deleted (i.e. `--purge` was passed).
type RepoRemovedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Purged      bool   `json:"purged"`
}
