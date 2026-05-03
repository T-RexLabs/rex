// Package server is the in-process central-node implementation —
// the bare-minimum sync.API surface (sync.API.1, .2, .3) that the
// local sync client can talk to during development.
//
// Scope is deliberately small: in-memory event store, no auth, no
// multi-tenancy, no Postgres, no Docker. The Postgres + Docker +
// RBAC + multi-tenancy work from central-node.yaml lands behind the
// same handler interfaces in a later commit set; until then the
// in-memory store is enough to drive the sync engine end-to-end in
// tests and on a developer's loopback.
//
// Per overview.SYS.1, this package is the central-only thin shell
// over types in internal/core (event records, sync proto types,
// identity). The shared core does not branch on local-vs-central.
package server
