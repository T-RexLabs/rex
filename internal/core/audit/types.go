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

	// Spec amendment lifecycle (audit.TYPES.1 "every spec amendment
	// proposed/accepted/rejected"; spec-format.AMEND.4). proposed
	// has no producer in v1 — the harness drafter writes amendment
	// files out-of-band (like a manual editor write), and tracking
	// arbitrary file appearance under _proposed/ would require
	// fsnotify on every workspace. accepted and rejected fire from
	// the CLI / web surfaces that mediate the lifecycle transition.
	EventTypeSpecAmendmentProposed = "spec.amendment.proposed"
	EventTypeSpecAmendmentAccepted = "spec.amendment.accepted"
	EventTypeSpecAmendmentRejected = "spec.amendment.rejected"

	// Remote lifecycle (audit.TYPES.1 "every remote attach/detach").
	EventTypeRemoteAttached = "remote.attached"
	EventTypeRemoteDetached = "remote.detached"

	// Hook lifecycle (audit.TYPES.1 "every hook invocation result").
	EventTypeHookCompleted = "hook.completed"

	// Harness brief attachment (audit.TYPES.1 "every harness
	// invocation start/end" surface). Records what workspace
	// context the harness saw — answer to the "what did the model
	// have when it ran" question.
	EventTypeHarnessBriefAttached = "harness.brief_attached"

	// Git-merged content rebase outcomes (sync.GIT.1-4). These are
	// audit-class via TYPES.1's "every spec change" / "every workspace
	// state change" clauses — a rebase mutates a workspace-scoped,
	// human-authored entity.
	EventTypeSyncGitRebased    = "sync.git.rebased"
	EventTypeSyncGitConflicted = "sync.git.conflicted"
	EventTypeSyncGitResolved   = "sync.git.resolved"

	// Auth + token lifecycle (identity-and-trust.AUTH.4 / TOKEN.5 /
	// SEC.2). Every successful auth handshake, every failed attempt,
	// and every rotation / revocation / replay event lands in the
	// audit log so an operator can answer "who logged in when, who
	// got denied, and was any token reused after rotation?" without
	// re-running queries against the central node's request log.
	EventTypeAuthSuccess       = "auth.success"
	EventTypeAuthFailure       = "auth.failure"
	EventTypeTokenIssued       = "token.issued"
	EventTypeTokenRefreshed    = "token.refreshed"
	EventTypeTokenRevoked      = "token.revoked"
	EventTypeAuthReplayAttempt = "auth.replay_attempt"

	// Org-admin mutations (CENTRAL.3 — "every action runs
	// through RBAC and writes audit entries"). The
	// `org.member.*` family fires from the central node's
	// member-administration surface, currently driven by the
	// /orgs/<id>/members POST handlers in internal/central/web
	// and the underlying PostgresStore mutators.
	EventTypeOrgMemberInvited     = "org.member.invited"
	EventTypeOrgMemberRoleChanged = "org.member.role_changed"
	EventTypeOrgMemberRemoved     = "org.member.removed"
	EventTypeOrgMemberJoined      = "org.member.joined"

	// Identity registration (identity-and-trust.AUTH.2.1 +
	// AUTH.2.2 — the invite-redeem path adds a fresh public key
	// to the central node's keystore + the Postgres
	// authorized_keys table). The audit row pairs with the
	// org.member.joined that fires alongside; together they
	// reconstruct "a brand-new key arrived and immediately joined
	// org X with role Y because of invite Z".
	EventTypeIdentityKeyRegistered = "identity.key_registered"
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
		EventTypeWorkspaceCreated:      {},
		EventTypeRepoAdded:             {},
		EventTypeRepoLinked:            {},
		EventTypeRepoRemoved:           {},
		EventTypeScheduleAdded:         {},
		EventTypeScheduleRemoved:       {},
		EventTypeWorkspaceArchived:     {},
		EventTypeWorkspaceUnarchived:   {},
		EventTypeWorkspaceDeleted:      {},
		EventTypeSpecCreated:           {},
		EventTypeSpecEdited:            {},
		EventTypeSpecAmendmentProposed: {},
		EventTypeSpecAmendmentAccepted: {},
		EventTypeSpecAmendmentRejected: {},
		EventTypeRemoteAttached:        {},
		EventTypeRemoteDetached:        {},
		EventTypeHookCompleted:         {},
		EventTypeHarnessBriefAttached:  {},
		EventTypeSyncGitRebased:        {},
		EventTypeSyncGitConflicted:     {},
		EventTypeSyncGitResolved:       {},
		EventTypeAuthSuccess:           {},
		EventTypeAuthFailure:           {},
		EventTypeTokenIssued:           {},
		EventTypeTokenRefreshed:        {},
		EventTypeTokenRevoked:          {},
		EventTypeAuthReplayAttempt:     {},
		EventTypeOrgMemberInvited:      {},
		EventTypeOrgMemberRoleChanged:  {},
		EventTypeOrgMemberRemoved:      {},
		EventTypeOrgMemberJoined:       {},
		EventTypeIdentityKeyRegistered: {},

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
		runner.EventTypeNodeSkipped:         {},
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

// SpecAmendmentEvent is the shared payload for the three amendment
// lifecycle events (proposed/accepted/rejected). AmendmentFor is the
// target spec id, or "multi" when the amendment's target field is
// `multi`. AmendmentDate is the YYYY-MM-DD parsed from the amendment
// frontmatter. Stem is the filename without the .yaml extension and
// is the canonical handle the CLI / web surfaces use to refer to an
// amendment. FromPath records the on-disk location at event-fire
// time; ToPath is populated only on accepted (the destination under
// _proposed/_accepted/) and otherwise empty.
type SpecAmendmentEvent struct {
	WorkspaceID   string `json:"workspace_id"`
	Stem          string `json:"stem"`
	AmendmentFor  string `json:"amendment_for"`
	AmendmentDate string `json:"amendment_date,omitempty"`
	FromPath      string `json:"from_path"`
	ToPath        string `json:"to_path,omitempty"`
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

// HookCompletedEvent is the payload for EventTypeHookCompleted —
// fires for every hook invocation (per audit.TYPES.1's "every hook
// invocation result"). Both successes and skips are recorded; the
// triggering event's id stays in TriggerEventID so the audit log
// can correlate hook results back to the cause.
type HookCompletedEvent struct {
	WorkspaceID    string `json:"workspace_id"`
	HookName       string `json:"hook_name"`
	HookScope      string `json:"hook_scope"` // "workspace" or "global"
	HookPath       string `json:"hook_path"`
	TriggerEventID string `json:"trigger_event_id"`
	ExitCode       int    `json:"exit_code"`
	Skipped        bool   `json:"skipped,omitempty"`
	Reason         string `json:"reason,omitempty"`
	DurationMs     int64  `json:"duration_ms"`
}

// HarnessBriefAttachedEvent is the payload for
// EventTypeHarnessBriefAttached — fires once per harness run
// when Rex prepended a workspace brief to the prompt. Recording
// the brief lets audit readers answer "what context did the
// model have?" without re-deriving it from workspace state at
// audit-read time. Length is included verbatim alongside a
// short SHA-256 prefix so the event payload stays compact while
// still tying the audit row to the exact bytes the harness saw.
type HarnessBriefAttachedEvent struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
	NodeID      string `json:"node_id"`
	Harness     string `json:"harness"`
	BriefBytes  int    `json:"brief_bytes"`
	BriefSHA256 string `json:"brief_sha256"` // hex-encoded SHA-256 prefix (16 chars)
	Source      string `json:"source"`       // "default" or "override"
}

// SyncGitRebasedEvent is the payload for EventTypeSyncGitRebased —
// fires when `rex sync rebase` produced a clean merge for an entity.
// Carries enough provenance to replay: the (base, local, remote)
// revisions that fed the merge plus the merged-content revision.
type SyncGitRebasedEvent struct {
	WorkspaceID    string `json:"workspace_id"`
	Entity         string `json:"entity"`
	Remote         string `json:"remote"`
	BaseRevision   string `json:"base_revision,omitempty"`
	LocalRevision  string `json:"local_revision"`
	RemoteRevision string `json:"remote_revision"`
	MergedRevision string `json:"merged_revision"`
}

// SyncGitConflictedEvent is the payload for EventTypeSyncGitConflicted
// — fires when a rebase pass surfaced unresolvable hunks and wrote a
// `<file>.conflict` sidecar (sync.GIT.3). Hunks is the count of
// unresolved regions; the sidecar carries the per-hunk detail.
type SyncGitConflictedEvent struct {
	WorkspaceID    string `json:"workspace_id"`
	Entity         string `json:"entity"`
	Remote         string `json:"remote"`
	BaseRevision   string `json:"base_revision,omitempty"`
	LocalRevision  string `json:"local_revision"`
	RemoteRevision string `json:"remote_revision"`
	Hunks          int    `json:"hunks"`
}

// SyncGitResolvedEvent is the payload for EventTypeSyncGitResolved —
// fires from `rex sync resolve <file>` after the user's hand-edited
// file passes the marker-free check and the sidecar is cleared
// (sync.GIT.4). ResolvedRevision is the content hash of the post-
// resolution local content.
type SyncGitResolvedEvent struct {
	WorkspaceID      string `json:"workspace_id"`
	Entity           string `json:"entity"`
	Remote           string `json:"remote"`
	ResolvedRevision string `json:"resolved_revision"`
}

// AuthSuccessEvent fires from POST /auth/verify when the client's
// signature verifies and a token pair is issued (AUTH.4). Carries
// the identity that authenticated and the requested scope; never
// the token value itself.
type AuthSuccessEvent struct {
	Fingerprint string `json:"fingerprint"`
	Scope       string `json:"scope"`
	ChallengeID string `json:"challenge_id"`
	Hostname    string `json:"hostname"`
}

// AuthFailureEvent fires when /auth/verify rejects a request
// (AUTH.4). Reason is the structured reason — "bad_signature",
// "unknown_fingerprint", "challenge_invalid", etc. The fingerprint
// claimed by the client is included even when it doesn't match a
// registered key, so an operator can correlate repeated failed
// attempts back to a presented identity.
type AuthFailureEvent struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Reason      string `json:"reason"`
	ChallengeID string `json:"challenge_id,omitempty"`
}

// TokenIssuedEvent fires once per access+refresh pair issued via
// /auth/verify (TOKEN.5). TokenID is the content-addressable hash
// of the issued access token (16-char prefix); the raw value is
// never logged. ChainID groups all tokens that descend from a
// single original /auth/verify, so a replay-driven chain revoke
// can mark them all in one query.
type TokenIssuedEvent struct {
	Fingerprint      string `json:"fingerprint"`
	Scope            string `json:"scope"`
	TokenID          string `json:"token_id"`
	ChainID          string `json:"chain_id"`
	ExpiresAt        string `json:"expires_at"`
	RefreshExpiresAt string `json:"refresh_expires_at"`
}

// TokenRefreshedEvent fires from POST /auth/refresh on success
// (TOKEN.5). NewTokenID identifies the freshly-issued access
// token; OldTokenID identifies the refresh-token-prefix that the
// rotation invalidated.
type TokenRefreshedEvent struct {
	Fingerprint string `json:"fingerprint"`
	ChainID     string `json:"chain_id"`
	OldTokenID  string `json:"old_token_id"`
	NewTokenID  string `json:"new_token_id"`
	ExpiresAt   string `json:"expires_at"`
}

// TokenRevokedEvent fires from POST /auth/revoke on success
// (TOKEN.4 / TOKEN.5). Count records the number of tokens
// invalidated (1 for a single-token revoke; the size of the chain
// when All=true was passed). Reason hints at the trigger
// ("explicit", "replay", "expired_at_use").
type TokenRevokedEvent struct {
	Fingerprint string `json:"fingerprint"`
	ChainID     string `json:"chain_id,omitempty"`
	TokenID     string `json:"token_id,omitempty"`
	Count       int    `json:"count"`
	Reason      string `json:"reason"`
}

// AuthReplayAttemptEvent fires when a refresh token is presented
// after the rotation that already replaced it (SEC.2). Triggers a
// chain-wide revoke and gets surfaced to the audit log so an
// operator can see "this identity's chain was wiped because of a
// replay at <time> from <hostname>".
type AuthReplayAttemptEvent struct {
	Fingerprint string `json:"fingerprint"`
	ChainID     string `json:"chain_id"`
	OldTokenID  string `json:"old_token_id"`
}

// OrgMemberInvitedEvent is the payload for EventTypeOrgMemberInvited
// — fires when an org admin issues a new invite via the central
// web shell (or any future CLI/REST surface). The invite token
// itself never lands in the audit body; only the rendered
// fingerprint stub + role + invite id, enough for an operator to
// trace the chain when the invite is later redeemed.
type OrgMemberInvitedEvent struct {
	OrgID    string `json:"org_id"`
	InviteID string `json:"invite_id"`
	Role     string `json:"role"`
	Inviter  string `json:"inviter"`
}

// OrgMemberRoleChangedEvent is the payload for
// EventTypeOrgMemberRoleChanged — fires when an admin promotes
// or demotes a member via the central web shell. The from/to
// pair lets reviewers reconstruct the role timeline without
// replaying the prior membership state.
type OrgMemberRoleChangedEvent struct {
	OrgID       string `json:"org_id"`
	Fingerprint string `json:"fingerprint"`
	FromRole    string `json:"from_role"`
	ToRole      string `json:"to_role"`
	ChangedBy   string `json:"changed_by"`
}

// OrgMemberRemovedEvent is the payload for
// EventTypeOrgMemberRemoved — fires when an admin removes a
// member from the org. Includes the prior role so reviewers can
// see what access was revoked.
type OrgMemberRemovedEvent struct {
	OrgID       string `json:"org_id"`
	Fingerprint string `json:"fingerprint"`
	PriorRole   string `json:"prior_role"`
	RemovedBy   string `json:"removed_by"`
}

// OrgMemberJoinedEvent is the payload for
// EventTypeOrgMemberJoined — fires when an invite is redeemed
// and a new membership row lands. InviteID cross-references the
// matching org.member.invited audit row, completing the
// invite → join lifecycle for reviewers.
type OrgMemberJoinedEvent struct {
	OrgID       string `json:"org_id"`
	Fingerprint string `json:"fingerprint"`
	Role        string `json:"role"`
	InviteID    string `json:"invite_id"`
}

// IdentityKeyRegisteredEvent is the payload for
// EventTypeIdentityKeyRegistered — fires the first time a
// fingerprint lands in the central node's authorized_keys
// table. Source records which path registered it; v1 only
// emits "invite-redeem" but the field leaves room for an
// admin-paste / SCIM-imported path later. InviteID is the
// invite that authorised the registration, so a reviewer can
// chase the chain back to the issuing admin.
type IdentityKeyRegisteredEvent struct {
	Fingerprint string `json:"fingerprint"`
	Handle      string `json:"handle,omitempty"`
	Source      string `json:"source"`
	InviteID    string `json:"invite_id,omitempty"`
}
