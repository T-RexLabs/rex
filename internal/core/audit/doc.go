// Package audit is the type-level marker layer over events.log that
// makes audit.STORE.* enforceable.
//
// The audit log is not a separate file: per audit.STORE.1, audit
// entries live in the same events.log as other event-sourced
// entities. Audit-class events are distinguished by the event type
// name being in this package's registry.
//
// Append-only enforcement (audit.STORE.2) is structural: this
// package exposes only Append; there is no Update or Delete code
// path. Combined with the file-level O_APPEND on events.log, no API
// surface mutates an audit row. The Postgres-role split required by
// audit.STORE.3 lives on the central node, which does not exist yet.
//
// Signatures (audit.SEC.1, SEC.3) are deferred until cross-node sync
// lands; v1 local events can be unsigned because they never leave
// the originating disk. The Appender takes a Signer parameter so the
// signature path drops in without an API change.
//
// # Catalog of audit-class event types (audit.TYPES.1 / TYPES.2)
//
// Every type below is registered in auditEventTypes; runtime lookups
// go through IsAuditEvent() and EventTypes(). The catalog is the
// "Enumerate and document every event type that becomes an audit
// entry" deliverable. Types reserved by audit.TYPES.1 but not yet
// emitted are listed at the bottom under "Reserved" with a pointer
// to the task that ships them.
//
// Workspace lifecycle:
//
//	workspace.created       payload: WorkspaceCreatedEvent
//	  fires from `rex workspace init` after .rex/ is written.
//	workspace.archived      payload: WorkspaceStateChangedEvent
//	workspace.unarchived    payload: WorkspaceStateChangedEvent
//	workspace.deleted       payload: WorkspaceStateChangedEvent
//	  fire from `rex workspace archive/unarchive/delete`
//	  (workspace.LIFE.3 / LIFE.3.1).
//
// Repo attach (workspace.REPO.4.1):
//
//	repo.added              payload: RepoAddedEvent
//	  fires from `rex repo add` after the clone succeeds.
//	repo.linked             payload: RepoLinkedEvent
//	  fires from `rex repo link`.
//	repo.removed            payload: RepoRemovedEvent
//	  fires from `rex repo remove`; Purged records the --purge flag.
//
// Schedule lifecycle (cli.SCHED.* + execution.SCHED.*):
//
//	schedule.added          payload: ScheduleAddedEvent
//	  fires from `rex schedule add`.
//	schedule.removed        payload: ScheduleRemovedEvent
//	  fires from `rex schedule remove`.
//
// Spec lifecycle (audit.TYPES.1 "every spec change"):
//
//	spec.created            payload: SpecCreatedEvent
//	  fires from `rex spec create`.
//	spec.edited             payload: SpecEditedEvent
//	  fires from `rex spec edit` after $EDITOR returns; HasErrors
//	  records the post-edit validation outcome.
//
// Remote lifecycle (audit.TYPES.1 "every remote attach/detach"):
//
//	remote.attached         payload: RemoteAttachedEvent
//	  fires from `rex remote add`.
//	remote.detached         payload: RemoteDetachedEvent
//	  fires from `rex remote remove`.
//
// Hook lifecycle (audit.TYPES.1 "every hook invocation result"):
//
//	hook.completed          payload: HookCompletedEvent
//	  fires for every hook execution dispatched in response to
//	  another event. Both successes (exit_code recorded) and skips
//	  (Skipped + Reason populated) land. TriggerEventID points at
//	  the originating event so audit-log readers can correlate
//	  cause-and-effect.
//
// Run lifecycle (re-exported from internal/core/runner so the
// registry has a single source of truth — execution.DAG.2):
//
//	run.started             payload: runner.RunStartedEvent
//	run.completed           payload: runner.RunCompletedEvent
//	run.cancelled           payload: runner.RunCancelledEvent
//	run.aborted             payload: runner.RunAbortedEvent
//	node.started            payload: runner.NodeStartedEvent
//	node.succeeded          payload: runner.NodeSucceededEvent
//	node.failed             payload: runner.NodeFailedEvent
//	node.retried            payload: runner.NodeRetriedEvent
//	node.skipped            payload: runner.NodeSkippedEvent
//	  fires when an outgoing edge's predicate rejected this node
//	  (execution.PRIM.5).
//	permission.requested    payload: runner.PermissionRequestedEvent
//	permission.granted      payload: runner.PermissionGrantedEvent
//	permission.denied       payload: runner.PermissionDeniedEvent
//
// Reserved (audit.TYPES.1 names them; producers ship in their own
// task PRs):
//
//	tool.invoked            (tools.* — MCP, deferred)
//	rbac.*                  (identity-and-trust.rbac-engine, todo)
//	auth.success / failure  (identity-and-trust.handshake-protocol —
//	                         producer not yet wired)
//	token.issued / refreshed / revoked
//	                        (identity-and-trust.token-lifecycle, todo)
//	sync.push / sync.pull   (sync.sync-api-client — producer not yet
//	                         wired)
//	(hook.completed graduated to "Hook lifecycle" above; producer
//	wired 2026-05-08.)
//	dispatch.*              (still-soft surface; ships when scheduled
//	                         dispatches grow distinct semantics from
//	                         RunStartedEvent.Trigger)
//
// Schema evolution: every event type carries EventVersion=1; future
// changes are additive (overview.SYS.4) — readers skip unknown
// fields per overview.SYS.3.
package audit
