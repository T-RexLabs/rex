# Deploying rex-central

This directory holds the bundled v1 deployment recipe for
`rex-central`, satisfying the `docker-compose-base` task in
`specs/central-node.yaml`.

## Layout

- `Dockerfile` — builds a static, cgo-free `rex-central` image
  on top of `gcr.io/distroless/static-debian12:nonroot`.
- `docker-compose.yml` — base compose file with three services:
  `rex-central`, `postgres`, and (under the `tls` profile)
  `caddy`.
- `docker-compose.dev.yml` — overlay for local development:
  publishes Postgres on `:55432`, runs the binary via
  `go run` against a bind-mounted source tree.
- `central.toml.example` — template for `/etc/rex/central.toml`
  inside the container. Holds non-secret defaults.
- `.env.example` — template for the `.env` file the compose
  stack reads. Holds secrets (Postgres password, public host
  for TLS).
- `Caddyfile` — Caddy 2 config used by the optional `tls`
  profile.

## Quickstart

```sh
cd deploy
cp central.toml.example central.toml
cp .env.example .env
$EDITOR .env       # set REX_PG_PASSWORD

# Production (no host port for Postgres, no host port for
# rex-central; expect a TLS terminator in front).
docker compose up -d

# Production with the bundled Caddy TLS terminator:
$EDITOR .env       # set REX_CENTRAL_HOST=your.host
docker compose --profile tls up -d
```

## Local development

```sh
cd deploy
cp central.toml.example central.toml
cp .env.example .env

docker compose -f docker-compose.yml \
               -f docker-compose.dev.yml up
```

This exposes Postgres on `127.0.0.1:55432` and `rex-central` on
`127.0.0.1:8080`, runs the binary via `go run` so source edits
take effect on container restart, and skips Caddy.

A local rex CLI can then attach the running central:

```sh
rex remote add primary http://127.0.0.1:8080
```

## Backups

`rex-central serve` runs a scheduled `pg_dump` whenever
`backup.dir` and `backup.cadence` are set in `central.toml`
(or via the matching `REX_CENTRAL_BACKUP_*` env vars). The
bundled compose file points the dump directory at the
`rex-backups` named volume so dumps survive `docker compose
down`.

One-shot snapshot:

```sh
docker compose exec rex-central rex-central backup
# → wrote /var/lib/rex/backups/rex-central-20260504T120000Z.dump in 250ms
```

Restore:

```sh
docker compose down
docker compose exec rex-central rex-central restore \
    --from /var/lib/rex/backups/rex-central-20260504T120000Z.dump
docker compose up -d
```

The restore wraps `pg_restore --clean --if-exists` and
validates the dump's PGDMP magic header before applying.
Bare-metal deployments need `postgresql-client` on PATH for
both `backup` and `restore` (the bundled image already has
it).

Per `BACKUP.2` only Postgres data is captured; transcript
files on the binary's `rex-state` volume are the operator's
responsibility — back them up alongside `rex-backups` with
your standard tooling.

## What's NOT here yet

Per `central-node.yaml`'s tasks block, the following are
separate PRs that build on top of the deployment recipe:

- **TENANT.\*** — multi-tenancy (`org_id` columns + RLS).
- **BOOT.\*** — first-run admin bootstrap.
- **DB.4** — Postgres FTS index for the search surface.
- **IDP-CENTRAL** — SSO/IdP bridging (deferred).

## Bare-metal deployment

`overview.SYS.1` and `central-node.DEPLOY.5` require the binary
to also run without Docker. That works the same way it always
did:

```sh
make build
./bin/rex-central serve --config /etc/rex/central.toml
```

The same precedence applies (defaults → config → env → flags).
Mount your authorized-keys TOML where `auth.keys_file` in the
config points; back the binary with a Postgres reachable via
`db.dsn`.
