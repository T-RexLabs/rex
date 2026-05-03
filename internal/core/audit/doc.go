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
package audit
