# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

The Go module `github.com/asabla/rex` is built out to a v1 daily-driveable shape. All six build-order steps below have shipped; the local node is usable end-to-end (workspace bootstrap → spec author/validate → run with shell + spec_validate + harness primitives → audit log → sync push/pull against a central node → snapshots). Two binaries: `cmd/rex` (local CLI + embedded web UI via `rex serve`) and `cmd/rex-central` (in-process central node).

Major shipped post-trunk pieces beyond the build order: hooks dispatcher, log tail, spec template scaffolding, FTS5 search across events + specs, embedded web UI (`/`, `/specs`, `/specs/<id>`, `/runs`, `/runs/<id>` with SSE live tail, `/audit`, `/remotes`).

### Common commands

- Build both binaries: `make build` (outputs to `bin/`)
- Run all tests: `make test` (or `make test-race`)
- Vet: `make vet`
- Format: `make fmt`
- Lint: `make lint` (requires `golangci-lint` installed locally; CI runs it via `golangci/golangci-lint-action`)
- Sync modules: `make tidy`
- Run a single test: `go test -run TestName ./path/to/pkg` (e.g. `go test -run TestRunVersion ./cmd/rex`)
- Run a single binary directly: `go run ./cmd/rex --version`
- Validate every spec strictly: `go run ./cmd/rex spec validate specs/*.yaml` (zero errors, zero warnings is the bar)
- Start the local web UI: `go run ./cmd/rex serve` (binds 127.0.0.1:7474 by default; loopback-only)
- Run with the Postgres-backed central tests: `make pg-up && make test-pg` (starts a local Postgres container, runs the full suite with `REX_PG_TEST_DSN` set so the central `PostgresStore` tests don't skip; `make pg-down` cleans up)
- Run rex-central with persistence: `rex-central serve --db 'postgres://...'` — without `--db` events go to an in-memory store and are lost on restart

CI lives in `.github/workflows/ci.yml` and runs build, vet, race tests, `go mod tidy` drift check, and golangci-lint on every push and PR.

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

## Build order — done

All six trunk steps shipped. Listed here as the read order if you're navigating the codebase for the first time:

1. **ACP client layer + minimal event-log store.** `internal/core/acp/`, `internal/core/storage/eventlog/`. Covers `execution.ACP.*`, `storage.EVENTS.*`.
2. **Workflow runner skeleton.** `internal/core/runner/` with `primshell`, `primspec`, `primharness` packages. Three node types, event-sourced. Covers `execution.DAG.*`, `execution.PRIM.1-3`.
3. **Spec format + validator.** `internal/core/specfmt/`. `rex spec validate` is wired and 14 real specs in `specs/` pass strict validation. Covers `spec-format.*`.
4. **Local CLI + hooks.** Cobra commands under `internal/local/cli/`; hooks dispatcher under `internal/core/hooks/`. Covers `cli.*`, `hooks.*`.
5. **Sync protocol.** `internal/core/sync/proto/` (wire types) + `internal/local/sync/` (client + watermarks) + `internal/central/server/` (in-process server + auth). Push-first ordering with conflict semantics. Covers `sync.*`.
6. **Snapshots.** `internal/core/snapshot/`. Covers `storage.SNAP.*`.

Post-trunk shipped pieces (do not re-derive — read the existing code):

- **Search:** FTS5 index over events + specs in `internal/core/search/`.
- **Identity + audit:** `internal/core/identity/` (ed25519 keypairs, signer, actor), `internal/core/audit/` (audit registry + appender).
- **Web UI:** `internal/local/web/` — embedded templates (`html/template`) + static assets via `embed.FS`. Routes: `/`, `/specs`, `/specs/<id>`, `/runs`, `/runs/<id>` (live SSE), `/audit`, `/remotes`. Vanilla-JS `htmx-ext-sse` shim at `static/htmx-ext-sse.js` is purpose-built for the run-detail live tail; swap in upstream htmx-ext-sse@2.2.x bytes for richer behaviour without changing template attributes. JS-disabled view degrades gracefully (web-ui.ACCESS.3).
- **Remotes registry:** `internal/local/remotes/` (`~/.config/rex/remotes.toml`).

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

## Project conventions

- **Go module path:** `github.com/asabla/rex`.
- **Go version:** pinned via `go.mod` (currently 1.23.0 — bumped from 1.22.1 to avoid a macOS 26 LC_UUID linker incompatibility); CI uses `go-version-file: go.mod`. Bump in `go.mod` and CI follows.
- **No-cgo guarantee:** `overview.ENG.2` forbids cgo in the local binary. SQLite goes through `modernc.org/sqlite` (pure Go). Do not add cgo deps.
- **Linter:** `golangci-lint` with config at `.golangci.yml`.
- **Tests:** stdlib `testing`. Determinism is required for sync, executor, and audit-log tests — inject time and randomness, never read the environment in test bodies (`overview.ENG.4`).
- **Commit format:** Conventional Commits with ACID citations.
- **Branches:** trunk-based, short-lived feature branches, PRs into `main`.

### Spec amendments

When a spec needs to change:

1. Write the proposed amendment to `specs/_proposed/<spec-id>-amendment-<YYYY-MM-DD>.yaml` and surface it for human signoff. Do not edit the spec yet.
2. After approval, fold the change into the spec and move the proposed file to `specs/_accepted/` (preserves the audit trail). Do not delete it.
3. Re-run `go run ./cmd/rex spec validate specs/*.yaml` and confirm 0 errors / 0 warnings before committing.

## When in doubt

Ask. Specs that look complete often have soft spots that only become visible when you try to implement them — surfacing those is part of the job.
