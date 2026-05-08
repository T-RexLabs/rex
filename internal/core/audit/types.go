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

	// Schedule lifecycle (cli.SCHED.* + execution.SCHED.*). Schedule
	// fires manifest as RunStartedEvent.Trigger; only register/
	// unregister actions are first-class events (audit.TYPES.1's
	// "every workspace state change" clause).
	EventTypeScheduleAdded   = "schedule.added"
	EventTypeScheduleRemoved = "schedule.removed"

	// Workspace state transitions (workspace.LIFE.3 / LIFE.3.1).
	// Audit-class via TYPES.1 "every workspace state change".
	EventTypeWorkspaceArchived   = "workspace.archived"
	EventTypeWorkspaceUnarchived = "workspace.unarchived"
	EventTypeWorkspaceDeleted    = "workspace.deleted"

	// Spec lifecycle (audit.TYPES.1 "every spec change"). Edits
	// elsewhere — direct text-editor or git pull — flow through
	// content sync rather than these events; spec.created and
	// spec.edited fire only when the rex CLI mediates the change.
	EventTypeSpecCreated = "spec.created"
	EventTypeSpecEdited  = "spec.edited"

	// Remote lifecycle (audit.TYPES.1 "every remote attach/detach").
	EventTypeRemoteAttached = "remote.attached"
	EventTypeRemoteDetached = "remote.detached"
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
		EventTypeWorkspaceCreated:    {},
		EventTypeRepoAdded:           {},
		EventTypeRepoLinked:          {},
		EventTypeRepoRemoved:         {},
		EventTypeScheduleAdded:       {},
		EventTypeScheduleRemoved:     {},
		EventTypeWorkspaceArchived:   {},
		EventTypeWorkspaceUnarchived: {},
		EventTypeWorkspaceDeleted:    {},
		EventTypeSpecCreated:         {},
		EventTypeSpecEdited:          {},
		EventTypeRemoteAttached:      {},
		EventTypeRemoteDetached:      {},

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

// ScheduleAddedEvent is the payload for EventTypeScheduleAdded —
// fires from `rex schedule add`.
type ScheduleAddedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	TriggerKind string `json:"trigger_kind"`
}

// ScheduleRemovedEvent is the payload for EventTypeScheduleRemoved
// — fires from `rex schedule remove`.
type ScheduleRemovedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
}

// WorkspaceStateChangedEvent is the shared payload shape for
// workspace.archived / workspace.unarchived / workspace.deleted.
// `from` and `to` records the transition so a replayer can
// reconstruct the state machine without consulting workspace.yaml.
type WorkspaceStateChangedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	From        string `json:"from"`
	To          string `json:"to"`
	At          string `json:"at"`
}

// SpecCreatedEvent is the payload for EventTypeSpecCreated — fires
// from `rex spec create` after the new YAML has been written.
type SpecCreatedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	SpecID      string `json:"spec_id"`
	Path        string `json:"path"`
	Template    string `json:"template,omitempty"`
}

// SpecEditedEvent is the payload for EventTypeSpecEdited — fires
// from `rex spec edit` after $EDITOR exits and the spec re-validates.
// HasErrors is the post-edit validation outcome; when true, the
// edit is preserved on disk but flagged for the user to fix.
type SpecEditedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	SpecID      string `json:"spec_id"`
	Path        string `json:"path"`
	HasErrors   bool   `json:"has_errors"`
}

// RemoteAttachedEvent is the payload for EventTypeRemoteAttached —
// fires from `rex remote add` after the registry write succeeds.
// URL is captured at attach time; later URL changes via re-add
// emit a fresh attached event rather than a separate "updated".
type RemoteAttachedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
}

// RemoteDetachedEvent is the payload for EventTypeRemoteDetached
// — fires from `rex remote remove`.
type RemoteDetachedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
}
