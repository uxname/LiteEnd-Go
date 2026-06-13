# LiteEnd-Go

Lightweight, fast, production-ready backend template in Go — a 1:1 port of the
NestJS/TypeScript **LiteEnd** template, with the GraphQL API contract preserved
so existing frontends work unchanged.

## TL;DR

- **What:** GraphQL + REST backend with OIDC/JWT auth, profiles, file uploads,
  background jobs, i18n, health checks, and DB backups.
- **Stack:** Go 1.26 · [chi](https://github.com/go-chi/chi) ·
  [gqlgen](https://gqlgen.com) · [pgx](https://github.com/jackc/pgx) +
  [sqlc](https://sqlc.dev) · [goose](https://github.com/pressly/goose) migrations ·
  [asynq](https://github.com/hibiken/asynq) (Redis) · [go-redis](https://github.com/redis/go-redis) ·
  [coreos/go-oidc](https://github.com/coreos/go-oidc) · `slog` · go-i18n.
- **Run it (one command):**
  ```bash
  cp .env.example .env && docker compose up --build
  ```
  Then open the GraphQL playground at <http://localhost:4000/playground>.
- **Arch Linux note:** the `task` runner is packaged as `go-task` — use
  `go-task <name>` everywhere this README says `task <name>` (the `Taskfile.yml`
  is identical).
- **Dev mock auth:** with `OIDC_MOCK_ENABLED=true` (default in `.env.example`)
  every request is authenticated as a mock `USER`+`ADMIN` user — no token needed.
  Send `x-mock-sub: <oidcSub>` to impersonate a specific profile.

## Features

| Area | Detail |
|---|---|
| GraphQL | `me`, `updateProfile`, `addTestJob`, admin `debug`/`echo`/`testTranslation`, `profileUpdated` subscription (WebSocket, `graphql-transport-ws`) |
| REST | `POST /upload` (JWT, images ≤5 MB, ≤10 files), `GET /uploads/*` (public, path-traversal safe), `GET /health` |
| Auth | OIDC/JWT via JWKS (RS256/ES384), find-or-create profile, Redis cache, RBAC, dev mock mode |
| Queue | asynq "test" queue: dedup by message, retries, concurrency 5 (dashboard: Asynqmon) |
| i18n | en/ru via `Accept-Language`, English fallback |
| Observability | structured `slog` JSON logs with secret redaction, request IDs |
| Backups | scheduled `pg_dump` with rotation (`cmd/dbbackup`) + restore (`cmd/dbrestore`) |

## Requirements

- Go 1.26+
- Docker (for `docker compose` and integration tests via testcontainers)
- [Task](https://taskfile.dev) (optional, for the `task` shortcuts)

## Quick start

### With Docker (recommended)

```bash
cp .env.example .env
docker compose up --build
```

Services (bound to `127.0.0.1`): app `:4000`, Postgres `:5432`, Redis `:6379`,
pgweb DB browser `:5100`, RedisInsight `:5200`, Asynqmon queue dashboard `:5300`.
A links page for all of these is served at `/dev`.

> **Admin auth — no anonymous access.** Every dashboard (pgweb, RedisInsight,
> Asynqmon) sits behind a Caddy Basic-Auth proxy, and the app's own dev pages
> (`/dev`, `/playground`, `/swagger`, `/openapi.json`) require the same
> credentials. Defaults: `admin` / `admin` (`ADMIN_USER` / `ADMIN_PASSWORD`; the
> proxy also needs `ADMIN_PASSWORD_HASH`). Change them before any non-local use.
> Public endpoints (`/graphql`, `/upload`, `/uploads/*`, `/health`) stay open.

> **Persistent state.** Application data you browse — `./data/uploads` and
> `./data/database_backups` — is host-mounted (a one-shot `data_init` container
> chowns them for the non-root app/backup uids). Postgres and Redis keep their
> internals in named volumes (`postgres`, `redis`): those files are root-owned
> `0700` and not host-readable anyway, and bind-mounting them would break local
> `go test ./...` (the toolchain can't walk a root-owned `./data/postgres`).

### Locally

```bash
cp .env.example .env          # point DATABASE_HOST/REDIS_HOST at running services
task gen                      # sqlc + gqlgen codegen
task run                      # migrations run automatically at startup
```

## Configuration

All config comes from environment variables (see `.env.example`). Key ones:

| Var | Purpose |
|---|---|
| `PORT` | HTTP port (default 4000) |
| `CORS_ORIGIN` | comma-separated allowed origins |
| `DATABASE_*` | Postgres connection |
| `REDIS_*` | Redis connection |
| `OIDC_ISSUER` / `OIDC_AUDIENCE` / `OIDC_JWKS_URI` | token validation |
| `OIDC_MOCK_ENABLED` | dev-only auth bypass (rejected in production) |
| `BACKUP_*` | backup dir, interval (Go duration, e.g. `24h`), rotation, format, compression |

## Commands (Taskfile)

```bash
task gen            # run sqlc + gqlgen code generation
task build          # build server + dbbackup + dbrestore into ./bin
task run            # run the server
task test           # unit tests
task test:integration  # integration/e2e tests (testcontainers; needs Docker)
task test:cov       # coverage report
task lint           # golangci-lint
task fmt            # gofumpt
task migrate        # apply migrations (DATABASE_URL=... task migrate)
```

## Project structure

```
cmd/
  server/       # HTTP application entrypoint (+ -healthcheck flag)
  dbbackup/     # scheduled pg_dump tool
  dbrestore/    # restore a backup file
internal/
  app/          # composition root (used by main and integration tests)
  config/       # typed env config + constants
  server/       # chi router + middleware stack + lifecycle
  middleware/   # request-id, recover, realip, logging, ratelimit, secure, body-limit
  auth/         # OIDC/JWKS verifier, JWT middleware, context, RBAC, mock mode
  db/           # pgx pool, programmatic goose migrations, sqlc queries
  redis/        # go-redis client, cache + pub/sub helpers
  profile/      # profile service (cache) + Redis pub/sub for subscriptions
  upload/       # multipart upload + safe file serving
  queue/        # asynq client + worker
  graph/        # gqlgen handler, resolvers, logging extension, error presenter
  i18n/         # go-i18n bundle + Accept-Language middleware
  health/       # health checks (db/redis/memory)
  backup/       # pg_dump/restore logic
  devtools/     # /dev launcher, Swagger UI, OpenAPI spec, dev-page CSP
db/
  migrations/   # goose SQL migrations (embedded)
  queries/      # sqlc SQL queries
Caddyfile       # Basic-Auth reverse proxy for the admin dashboards
docker-compose.yml
```

## API

- **GraphQL:** `POST /graphql` (and `GET` for the playground at `/playground`).
  Subscriptions over WebSocket at the same endpoint using the
  `graphql-transport-ws` subprotocol.
- **OpenAPI / Swagger UI:** `/swagger` (spec at `/openapi.json`).
- **Queue dashboard:** Asynqmon (separate container, `:5300`).

## Testing

```bash
task test                # fast unit tests (mocks, no external deps)
task test:integration    # full e2e against real Postgres + Redis (testcontainers)
```

Integration tests cover the GraphQL contract (queries, mutations, the
`profileUpdated` subscription), uploads, the queue, i18n, and health.

## Deployment

The production image is a multi-stage build onto `distroless/static` (non-root,
no shell). Database migrations are embedded and applied programmatically at
startup with retry. Container health is reported via `server -healthcheck`.

## License

MIT — see [LICENSE](LICENSE).
