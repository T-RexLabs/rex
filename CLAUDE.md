# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

This repo currently contains only the v1 specifications under `specs/` plus `readme.md`. There is no Go code, no `go.mod`, no build, lint, or test tooling yet. The first implementation task (`overview.bootstrap-repo`) is to scaffold the Go module, repo layout, and CI — do not fabricate commands that do not yet exist.

Once Go scaffolding lands, update this file with the real `go build`, `go test`, `golangci-lint run`, and single-test invocations.

## Read these first, every session

1. `readme.md` — read order, locked architectural choices, v1 scope cuts
2. `specs/overview.yaml` — system-wide invariants (`SYS.*`), naming (`NAME.*`), scope boundary (`SCOPE.*`), and the `ENG.*` / `SEC.*` constraints that apply to everything
3. `specs/spec-format.yaml` — the YAML schema all specs use (it describes itself); required to read or write any other spec correctly
4. The spec(s) relevant to the current task

The specs are the contract. They use ACID references of the form `<spec-id>.<COMPONENT>.<requirement-id>` (e.g. `sync.ORDER.3`, `overview.SEC.2`). Cite these in commits, PR descriptions, comments, and tests.

## Rules of engagement

- **One task from one spec per PR.** The `tasks` blocks in each spec were sized for this. Do not bundle.

- **Cite ACIDs in commits and PRs.** Conventional Commits format: `feat(sync): implement HLC tiebreaking [sync.ORDER.3]`. The PR description should list every ACID the change satisfies.

- **Read the `constraints` block, not just `components`.** Engineering and security invariants live there and apply to every task in the spec. Tasks do not reference them, so they are easy to miss. `overview.ENG.*` and `overview.SEC.*` apply codebase-wide.

- **Stop on `-note` fields and deferred decisions.** Anywhere a spec says "deferred", "open question", or has a `-note` suffix on a requirement ID, that is an unresolved decision. Do not pick a default — ask.

- **Do not silently edit specs.** If a spec is wrong, incomplete, or contradicts another, write a proposed amendment to `specs/_proposed/<spec-id>-amendment-<date>.yaml` and surface it. Specs change only with explicit human signoff.

- **Do not skip ahead.** Half-done verticals are the failure mode here. Finish the current task, write tests, hand it back.

- **No new external runtime dependencies.** `overview.ENG.1` forbids runtime services beyond SQLite (local) / Postgres (central). Adding a Go module dep is fine when justified — surface it in the PR description.

## Build order — do not deviate without asking

1. **ACP client layer + minimal SQLite event store.** Covers `execution.ACP.*`, `storage.EVENTS.*`. Goal: open a Claude Code session, capture every ACP frame as an event-log row, replay them. Target under 500 LOC.
2. **Workflow runner skeleton.** Three node types (`harness_invocation`, `shell_exec`, `spec_validate`). Single-tenant, single-workspace, no sync. Covers `execution.DAG.*`, `execution.PRIM.1-3`. Target under 1k LOC.
3. **Spec format + validator.** Implement `rex spec validate`. Then write three real specs against the format and revise the schema based on what breaks (this is `spec-format.write-three-real-specs`). Lock the v1 schema only after this step. Covers `spec-format.*`.
4. **Local CLI + hooks.** `rex workspace`, `rex spec`, `rex run`, `rex status`, `rex hooks list`. Daily-driveable for one human, no remote. Covers `cli.WS.*`, `cli.SPEC.*`, `cli.RUN.*`, `cli.STATUS.*`, `hooks.*`.
5. **Sync protocol.** Only after steps 1–4 are solid. Covers `sync.*`.
6. **Snapshots.** After sync. Covers `storage.SNAP.*`.

Web UI, multi-remote, scheduled work, connected tools, central node — all post-step-6. Do not start them earlier.

## High-level architecture

Rex is a decentralized, local-first management portal for agentic coding harnesses (Claude Code, Codex, OpenCode, Copilot, Cursor). A workspace is a container of intent — zero, one, or many repositories plus specs, transcripts, scheduled work, hooks, and connected tools. Work happens in five flavors (questions, non-spec interactions, spec work, management, scheduled work) sharing one interaction model and disambiguated by tags + permissions.

Locked architectural choices (from `readme.md`):

- **Language:** Go for both local and central; one module, shared core. Differences live behind build tags or a thin shell, never in core logic (`overview.SYS.1`).
- **Local persistence:** SQLite with FTS5.
- **Central persistence:** Postgres with Postgres FTS.
- **Transport:** HTTPS + Server-Sent Events. No plaintext fallback (`overview.SEC.3`).
- **Harness protocol:** ACP (Agent Client Protocol).
- **Tool protocol:** MCP (Model Context Protocol).
- **Workflow engine:** embedded event-sourced executor (~2-3k LOC), engine = fold over event log.
- **Identity:** ed25519 keypair + handle, Git-spirit (per-remote trust). No password auth (`overview.SEC.4`).
- **Sync model:** Git-style merge for human-authored content; event-sourced append-only for facts; per-node-derived for indexes/caches. Every persisted piece of state has a known sync category and the category is enforced at the storage layer (`overview.SYS.2`).
- **Conflict authority:** central authoritative; local rebases on reconnect.
- **Topology:** isolated central nodes; the local node is the only fan-out across remotes.
- **Offline:** full local-first. Remote dependence is a UX degradation, never a correctness one (`overview.SYS.6`).

Every event written anywhere has a `type` and `version` field; readers skip unknown types rather than erroring (`overview.SYS.3`). Schema evolution is additive only — no field removal, no semantic changes (`overview.SYS.4`).

## Naming conventions baked into the contract

From `overview.NAME.*`:

- Product and CLI command: `rex`
- On-disk per-workspace metadata directory: `.rex/`
- Per-workspace settings file: `workspace.yaml` (not `rex.yaml` — the file describes the workspace, not the tool)
- User-level config: `~/.config/rex/` on Linux, platform-equivalent elsewhere

Spec filenames in `specs/` are kebab-case lowercase (e.g. `spec-format.yaml`, `identity-and-trust.yaml`, `web-ui.yaml`) — the `readme.md` read order is the source of truth for these names.

## Out of scope for v1

Captured in `readme.md` and `overview.SCOPE.*` — do not implement:

- Central-side execution (deferred to v1.5)
- Worktree-based concurrency (serial-per-workspace in v1)
- Tool-call audit proxy (direct ACP `session/new` mcpServers in v1)
- Custom DAG node types, plugin loaders, custom CLI commands
- Pre-event gating hooks (only post-event observers in v1)
- Embeddings / semantic search (FTS only)
- Central-to-central federation (local node fans out)
- Hardware-backed key storage (software ed25519 only; signer interface leaves room for it later)

## Project conventions (apply once code lands)

- **Go module path:** TBD — confirm with the human before the first commit.
- **Minimum Go version:** latest stable.
- **Linter:** `golangci-lint` with config at `.golangci.yml`.
- **Tests:** stdlib `testing`. Determinism is required for sync, executor, and audit-log tests — inject time and randomness, never read the environment in test bodies (`overview.ENG.4`).
- **Commit format:** Conventional Commits with ACID citations.
- **Branches:** trunk-based, short-lived feature branches, PRs into `main`.

## When in doubt

Ask. Specs that look complete often have soft spots that only become visible when you try to implement them — surfacing those is part of the job.
