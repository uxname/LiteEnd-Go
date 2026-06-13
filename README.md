# LiteEnd-Go

A small, fast, ready-for-production backend starter written in Go. It speaks the
same GraphQL API as the original TypeScript **LiteEnd**, so an existing frontend
keeps working without changes.

## TL;DR

- **What you get:** a GraphQL + REST backend with login (OIDC/JWT), user
  profiles, file uploads, background jobs, translations (en/ru), health checks,
  and database backups.
- **Main tools:** Go 1.26 · [chi](https://github.com/go-chi/chi) (router) ·
  [gqlgen](https://gqlgen.com) (GraphQL) · [pgx](https://github.com/jackc/pgx) +
  [sqlc](https://sqlc.dev) (Postgres) · [goose](https://github.com/pressly/goose)
  (migrations) · [asynq](https://github.com/hibiken/asynq) (jobs, on Redis) ·
  `slog` (logs) · go-i18n (translations).
- **Run everything with one command:**
  ```bash
  cp .env.example .env && docker compose up --build
  ```
  Then open the GraphQL playground: <http://localhost:4000/playground>
  (login: `admin` / `admin`).
- **Develop locally with auto-reload:** `task setup` once, then `task dev`.
- **No login token needed in dev:** `OIDC_MOCK_ENABLED=true` (the default) signs
  you in as a fake `USER`+`ADMIN`. Send header `x-mock-sub: <id>` to act as a
  specific user.
- **On Arch Linux:** the `task` runner is the `go-task` package. Run `go-task`
  wherever this file says `task`.

## What's inside

| Area | What it does |
|---|---|
| GraphQL | `me`, `updateProfile`, `addTestJob`, admin-only `debug`/`echo`/`testTranslation`, and a `profileUpdated` live subscription (WebSocket) |
| REST | `POST /upload` (login required, images only, ≤5 MB, ≤10 files), `GET /uploads/*` (public, safe), `GET /health` |
| Login | OIDC/JWT checked against the provider's keys (JWKS); creates a profile on first login; roles come from the database; dev mock mode |
| Jobs | an asynq "test" queue with retries and de-duplication (dashboard: Asynqmon) |
| Translations | en/ru, chosen by the `Accept-Language` header, English as fallback |
| Logs | structured JSON logs that hide secrets and include a request id |
| Backups | scheduled `pg_dump` with rotation, plus a restore command |

## Before you start

You need:

- **Go 1.26+**
- **Docker** (for `docker compose` and for the integration tests)
- **Task** (optional but handy) — the command shortcuts below. On Arch it's the
  `go-task` package.

## Get it running

### Option A — everything in Docker (simplest)

```bash
cp .env.example .env
docker compose up --build
```

This starts: the app on `:4000`, Postgres on `:5432`, Redis on `:6379`, and three
admin dashboards. Open <http://localhost:4000/dev> for a page that links to all of
them.

### Option B — app on your machine, database in Docker (best for coding)

```bash
task setup     # copies .env, installs git hooks, generates code, starts DB+Redis, runs migrations
task dev       # runs the app and restarts it automatically when you change a .go file
```

`task dev` uses [wgo](https://github.com/bokwoon95/wgo) for auto-reload — save a
file and the server restarts on its own.

> **Logging in to the dashboards.** Every dashboard is protected — there is no
> anonymous access. The dashboards (pgweb, RedisInsight, Asynqmon) sit behind a
> password proxy, and the app's own dev pages (`/dev`, `/playground`, `/swagger`,
> `/openapi.yaml`) ask for the same login. Default: `admin` / `admin`. Change it
> before using this anywhere real. The public endpoints (`/graphql`, `/upload`,
> `/uploads/*`, `/health`) stay open.

> **Where data lives.** Files you can browse — `./data/uploads` and
> `./data/database_backups` — are stored on your machine. Postgres and Redis keep
> their own data in Docker volumes (don't put those in `./data` — they're
> root-owned and would break `go test ./...`).

## Everyday commands

Run `task --list` to see them all. The ones you'll use most:

| Command | What it does |
|---|---|
| `task setup` | First-time setup (env, hooks, codegen, DB, migrations) |
| `task dev` | Run the app with auto-reload |
| `task gen` | Regenerate code (after editing SQL or the GraphQL schema) |
| `task test` | Run fast unit tests |
| `task test:integration` | Run full tests against real Postgres + Redis (needs Docker) |
| `task lint` | Check code style and quality |
| `task fmt` | Auto-format the code |
| `task vuln` | Check dependencies for known security problems |
| `task migrate` | Apply database migrations |
| `task migration:create name=add_x` | Create a new migration file |
| `task db:reset` | Wipe and recreate the dev database |

## How you log in (dev vs real)

- **Dev (default):** `OIDC_MOCK_ENABLED=true`. You're automatically a fake user
  with `USER` and `ADMIN` roles — no token needed. Add header `x-mock-sub: alice`
  to pretend to be a specific person.
- **Real:** set `OIDC_MOCK_ENABLED=false` and point `OIDC_ISSUER`,
  `OIDC_AUDIENCE`, and `OIDC_JWKS_URI` at your provider (e.g. Logto). The app then
  checks the JWT the frontend sends. See `.env.example` for a worked example.

## Settings

All settings come from environment variables (see `.env.example`). The important ones:

| Variable | Meaning |
|---|---|
| `PORT` | which port the app listens on (default 4000) |
| `CORS_ORIGIN` | comma-separated list of allowed frontend origins |
| `DATABASE_*` | Postgres connection |
| `REDIS_*` | Redis connection |
| `OIDC_*` | login / token checking |
| `OIDC_MOCK_ENABLED` | dev-only login bypass (refused in production) |
| `ADMIN_USER` / `ADMIN_PASSWORD` | login for the dashboards and dev pages |
| `BACKUP_*` | backup folder, interval, how many to keep, format |

## The API

- **GraphQL:** `POST /graphql`. Try it in the playground at `/playground`.
  Live updates use a WebSocket on the same URL (`graphql-transport-ws`).
- **REST docs:** Swagger UI at `/swagger` (the spec file is at `/openapi.yaml`).
- **Jobs dashboard:** Asynqmon (its own container, port `:5300`).

## Where things live

```
cmd/         entry points: server, dbbackup, dbrestore
internal/
  app/        wires everything together (used by main and by tests)
  config/     reads settings from the environment
  server/     the chi router + middleware
  middleware/ request id, recovery, real IP, rate limit, security headers
  auth/       OIDC/JWT checking, login middleware, roles, mock mode
  db/         Postgres pool, migrations, generated SQL (sqlc)
  redis/      Redis client, cache, pub/sub
  profile/    the profile service (cache + live-update events)
  upload/     file uploads and safe file serving
  queue/      background jobs (asynq)
  graph/      GraphQL handler, resolvers, error formatting, logging
  i18n/       translations (en/ru)
  health/     the /health check
  backup/     pg_dump / restore logic
  devtools/   the /dev page, Swagger UI, OpenAPI spec
db/
  migrations/ database migrations (goose)
  queries/    SQL the code generator turns into Go (sqlc)
Caddyfile     password proxy for the dashboards
docker-compose.yml
```

## Adding your own code

- **A GraphQL field:** edit `internal/graph/schema.graphqls`, run `task gen`, then
  write the new resolver in `internal/graph/resolver/`.
- **A database query:** add it to `db/queries/*.sql`, run `task gen`, then call
  `database.Queries.<Name>`.
- **A migration:** `task migration:create name=add_something`, edit the new file
  in `db/migrations/`.

There's a deeper guide for contributors and AI agents in
[AGENTS.md](AGENTS.md) — read it before bigger changes.

## Testing

```bash
task test               # fast unit tests (fakes, no Docker)
task test:integration   # full tests against real Postgres + Redis (Docker)
```

The integration tests cover the GraphQL contract (queries, mutations, the
`profileUpdated` subscription), uploads, jobs, translations, and health.

## Deploying

The production image is a tiny [distroless](https://github.com/GoogleContainerTools/distroless)
build — no shell, runs as a non-root user. Migrations run automatically on
startup (with retries). The container reports health via `server -healthcheck`.

## License

MIT — see [LICENSE](LICENSE).
