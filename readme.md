# Rex — v1 specifications

Rex is a management portal for agentic coding harnesses (Claude Code, Codex, OpenCode, etc.). It is a decentralized application: local-first nodes that can connect to one or more central nodes for shared auditability, search, and dispatch.

This directory contains the v1 specifications. Each `.yaml` file is a self-contained spec that follows Rex's own spec format (see `specs/spec-format.yaml` — the format describes itself). Specs cross-reference each other via `extra.related_specs` and via ACID references like `sync.GIT.3` (meaning: spec `sync`, component `GIT`, requirement `3`).

## Read order

Top-down for the first pass; jump around afterwards.

1. `specs/overview.yaml` — umbrella spec, system-wide invariants, scope-cuts, name
2. `specs/spec-format.yaml` — the YAML spec format itself, ACIDs, validation, templates
3. `specs/identity-and-trust.yaml` — keypairs, handles, orgs (central-only), RBAC
4. `specs/workspace.yaml` — workspace model, repos, work types, states
5. `specs/storage.yaml` — on-disk layout, registry, snapshots, encryption
6. `specs/sync.yaml` — what syncs and how (Git-style merge + event-sourced split)
7. `specs/execution.yaml` — DAG executor, ACP harness invocation, runs
8. `specs/search.yaml` — indexing, FTS, gitignore-awareness, cross-workspace
9. `specs/tools.yaml` — MCP servers and app-level integrations
10. `specs/hooks.yaml` — file-based event observers
11. `specs/audit.yaml` — append-only audit log, retention, compaction
12. `specs/central-node.yaml` — Docker Compose deployment, multi-tenancy, Postgres
13. `specs/cli.yaml` — `rex` command surface
14. `specs/web-ui.yaml` — local + central htmx UIs

## Conventions used in these specs

- `metadata.state: draft` everywhere — these are the v1 contract proposals; flip to `active` once reviewed.
- `components` holds the acceptance criteria. Component IDs are uppercase, requirement IDs are numeric with optional `.subnum` for sub-requirements.
- ACID format: `<spec.id>.<COMPONENT>.<requirement-id>`. Stable forever; never renumber. Use `deprecated: true` on a requirement to retire it without removing the ID.
- `tasks` are the suggested implementation slices; each task `references` the requirements it satisfies.
- `constraints` blocks hold cross-cutting invariants (engineering, security, etc.) that apply alongside feature requirements.
- `extra.related_specs` lists ID strings (e.g. `sync`, `execution`) — these point at sibling files in this directory.

## Out of scope for v1

Captured here so Claude Code doesn't waste tokens implementing them:

- Central-side execution (deferred to v1.5 — see `execution.yaml` constraint EXEC-SCOPE)
- Worktree-based concurrency (serial-per-workspace in v1)
- Tool-call audit proxy (direct ACP `session/new` mcpServers in v1)
- Custom DAG node types, plugin loaders, custom CLI commands
- Pre-event gating hooks (only post-event observers in v1)
- Embeddings / semantic search (FTS only)
- Central-to-central federation (local node fans out)
- Hardware-backed key storage (software ed25519 only; signer interface leaves room for it later)

## Locked architectural choices

These come from the interview and are referenced by multiple specs:

- **Language:** Go for both local and central; shared core
- **Local persistence:** SQLite with FTS5
- **Central persistence:** Postgres with Postgres FTS
- **Transport:** HTTPS + Server-Sent Events
- **Harness protocol:** ACP (Agent Client Protocol)
- **Tool protocol:** MCP (Model Context Protocol)
- **Workflow engine:** embedded event-sourced executor (~2-3k LOC), engine = fold over event log
- **Central deployment:** Docker Compose
- **Identity:** ed25519 keypair + handle, Git-spirit (per-remote trust)
- **Sync model:** Git-style merge for human-authored content; event-sourced append-only for facts; per-node-derived for indexes/caches
- **Conflict authority:** central authoritative; local rebases on reconnect
- **Offline:** full local-first
- **Topology:** isolated central nodes; local node is the only fan-out
