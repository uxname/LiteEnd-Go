# LiteEnd-Go

A small, fast, ready-for-production backend starter written in Go. It speaks the
same GraphQL API as the original TypeScript **LiteEnd**, so an existing frontend
keeps working without changes.

New to the project? Jump to [New here? Read this first](#new-here-read-this-first)
for the 5-minute orientation before anything else.

## TL;DR

- **What you get:** a GraphQL + REST backend with login (OIDC/JWT), user
  profiles, file uploads, background jobs, translations (en/ru), and health
  checks.
- **Main tools:** Go 1.26 · [chi](https://github.com/go-chi/chi) (router) ·
  [gqlgen](https://gqlgen.com) (GraphQL) · [pgx](https://github.com/jackc/pgx) +
  [sqlc](https://sqlc.dev) (Postgres) · [goose](https://github.com/pressly/goose)
  (migrations) · [asynq](https://github.com/hibiken/asynq) (jobs, on Redis) ·
  `slog` (logs) · go-i18n (translations).

---

**To run the project, pick one path:**

### Path A — App on host, DB in Docker (recommended for development)
Requires [Go 1.26+](https://go.dev/dl), Docker, and `task` (or `go-task` on Arch).

```bash
task setup       # one-time: copy .env, hooks, codegen, start DB, run migrations
task start:dev   # hot-reload dev server — rebuilds on every .go change
```

Verify: <http://localhost:4000/readyz> → `{"status":"ok", ...}`

### Path B — Everything in Docker (needs only Docker, no Go required)
```bash
cp .env.example .env
docker compose up --build
```

Then open <http://localhost:4000/playground> (login: `admin` / `admin`).

---

- **Dev login is mocked by default** (`OIDC_MOCK_ENABLED=true`) — no real token
  needed. Send header `x-mock-sub: <id>` to act as a specific user.
- **On Arch Linux:** the `task` runner is the `go-task` package. Run `go-task`
  wherever this file says `task`.

## New here? Read this first

**What problem does this solve?** It's a starter kit, not a finished app. It
already wires up the boring-but-essential parts of a backend (database, login,
logging, jobs, file uploads) so you can spend your time on *your*
features instead of plumbing.

**What should I already know?** Basic Go, what an HTTP API is, and how to use a
terminal. You do **not** need to know GraphQL, OIDC, or code generation up front
— the mini-glossary below covers what matters, and you'll pick up the rest by
following the [walkthrough](#your-first-change-a-walkthrough).

**Mini-glossary** (the words you'll keep seeing):

| Term | In one sentence |
|---|---|
| **GraphQL** | An API style where the client asks for exactly the fields it wants in one request. Our API "shape" is defined in `internal/graph/schema.graphqls`. |
| **Resolver** | The Go function that produces the data for a GraphQL field. You write these by hand in `internal/graph/resolver/`. |
| **Code generation (codegen)** | Tools read a definition file and write Go code for you. We generate GraphQL plumbing from the schema (gqlgen) and type-safe DB code from SQL (sqlc). **Never edit generated files by hand** — change the source and re-run `task gen`. |
| **Migration** | A versioned SQL file that changes the database structure (e.g. add a table). They run automatically on startup. |
| **OIDC / JWT** | The login standard. In dev it's faked, so you don't need a real login server to start. |
| **Resolver vs generated** | You edit: the schema, SQL queries, and resolver bodies. The machine owns: everything under `generated/`, `model/models_gen.go`, and `db/sqlc/`. |

**How a request flows** (the big picture):

```
HTTP request
   → chi router + middleware   (internal/server, internal/middleware)
   → auth: who is this user?    (internal/auth — reads the token / mock)
   → GraphQL handler            (internal/graph)
   → a resolver                 (internal/graph/resolver — your code)
   → a service                  (e.g. internal/profile — business logic)
   → the database / cache       (internal/db via sqlc, internal/redis)
```

Read [AGENTS.md](AGENTS.md) once you want the deeper "why" behind the rules.

## What's inside

| Area | What it does |
|---|---|
| GraphQL | `me`, `updateProfile`, `addTestJob`, admin-only `debug`/`echo`/`testTranslation`, and a `profileUpdated` live subscription (WebSocket) |
| REST | `POST /upload` (login required, images only, ≤5 MB, ≤10 files; the file goes to object storage and you get its public URL back), plus the probes `GET /livez`, `GET /readyz` and `GET /health` |
| Login | OIDC/JWT checked against the provider's keys (JWKS); creates a profile on first login; roles come from the database; dev mock mode |
| Jobs | an asynq "test" queue with retries and de-duplication (dashboard: Asynqmon) |
| Translations | en/ru, chosen by the `Accept-Language` header, English as fallback |
| Logs | structured JSON logs that hide secrets and include a request id |

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

This starts: the app on `:4000`, Postgres on `:5432`, Redis on `:6379`, a
[Garage](https://garagehq.deuxfleurs.fr) object store for uploaded files (S3 API on
`:3900`, public file links on `:3902`), and three admin dashboards. Open
<http://localhost:4000/dev> for a page that links to all of them.

Garage configures itself on the first `up` — a small `garage-init` container writes
the cluster layout, the access key and the bucket, so there is nothing to run by
hand afterwards. It needs **Docker Engine 27.4 or newer**: it mounts the Garage
binary straight out of the Garage image (`type: image`, added in 27.4), which is the
only way to run that CLI — the image is built `FROM scratch` and has no shell.

### Option B — app on your machine, database in Docker (best for coding)

```bash
task setup         # copies .env, installs git hooks, generates code, starts DB+Redis, runs migrations
task start:dev     # runs the app with auto-reload — restarts on every .go change
```

`task start:dev` uses [wgo](https://github.com/bokwoon95/wgo) for auto-reload — save a
file and the server restarts on its own. It brings up Postgres and Redis but not the
object store, so run `docker compose up -d garage garage-init` too when you want to
try `POST /upload`; without it the app starts fine and only the upload fails.

> **First time? Sanity check.** After `task start:dev` is running, open
> <http://localhost:4000/readyz> — you should see `"status":"ok"` and every
> dependency listed as ok. Then open
> <http://localhost:4000/playground> (login `admin` / `admin`) and run
> `query { me { id roles } }`. If both work, your setup is good.

> **Logging in to the dashboards.** Every dashboard is protected — there is no
> anonymous access. The dashboards (pgweb, RedisInsight, Asynqmon) sit behind a
> password proxy, and the app's own dev pages (`/dev`, `/playground`, `/swagger`,
> `/openapi.yaml`) ask for the same login. Default: `admin` / `admin`. Change it
> before using this anywhere real. The public endpoints (`/graphql`, `/upload`,
> `/livez`, `/readyz`, `/health`) stay open.

> **Where data lives.** Nothing is written into the repo. Postgres, Redis and
> Garage each keep their data in a Docker volume (don't move those into `./data` —
> they're root-owned and would break `go test ./...`). Uploaded files go to Garage,
> not to the app's own filesystem, which is what lets several copies of the app
> share them. Browse them at the public link the upload returns.

## Your first change (a walkthrough)

Let's add a `version` field to the API so a client can ask the server which build
it's running. This shows the **schema → generate → resolve** loop you'll use for
almost every change.

1. **Describe it in the schema.** Open `internal/graph/schema.graphqls` and add a
   field to `Query` (always give it a description string — the schema is meant to
   be self-documenting):

   ```graphql
   type Query {
     # ...existing fields...

     "The running server build version"
     version: String!
   }
   ```

2. **Generate the plumbing.** Run:

   ```bash
   task gen
   ```

   gqlgen writes a new resolver stub for `version` and updates the generated
   files. (`task gen` also formats the output and never touches your hand-written
   resolver bodies.)

3. **Fill in the resolver.** Open the new stub in
   `internal/graph/resolver/` and return a value — the existing `Debug` resolver
   shows how to read app info. Build it to be sure it compiles:

   ```bash
   go build ./...
   ```

4. **Try it.** `task start:dev`, then in the playground run `query { version }`.

5. **Check before committing.** `task check` runs the same gate the
   `pre-commit` hook does (codegen freshness, format, lint, vuln, secrets).

The same loop applies to the database: edit a query in `db/queries/*.sql`, run
`task gen`, then call the generated `database.Queries.<Name>` from a service.

## Everyday commands

Run `task --list` to see them all. The ones you'll use most:

| Command | What it does |
|---|---|
| `task setup` | First-time setup (env, hooks, codegen, DB, migrations) |
| `task start:dev` | Run the app with auto-reload (wgo, hot-reload) |
| `task start:prod` | Run the app without hot-reload |
| `task gen` | Regenerate code (after editing SQL or the GraphQL schema) |
| `task check` | Full project gate — codegen, format, tidy, build, lint, vuln, secrets (runs on `pre-commit`) |
| `task test` | Run fast unit tests |
| `task test:all` | Run every test — unit + integration (needs Docker) |
| `task test:cov` | Every test plus the coverage floors — this is what runs on `pre-push` |
| `task test:integration` | Run only the full tests against real Postgres + Redis (needs Docker) |
| `task lint` | Check code style and quality |
| `task fmt` | Auto-format the code |
| `task vuln` | Check dependencies for known security problems |
| `task db:migrate` | Apply database migrations |
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
| `NODE_ENV` | **set it to exactly `production` in production.** That one value refuses `OIDC_MOCK_ENABLED`, requires a non-empty `CORS_ORIGIN`, and turns off GraphQL introspection and internal error messages. Default `development`; anything else (including `staging`) counts as non-production |
| `CORS_ORIGIN` | comma-separated list of allowed frontend origins — gates **both** HTTP CORS and the GraphQL WebSocket handshake |
| `DATABASE_*` | Postgres connection |
| `REDIS_*` | Redis connection |
| `OIDC_*` | login / token checking |
| `OIDC_MOCK_ENABLED` | dev-only login bypass (refused in production) |
| `ADMIN_USER` / `ADMIN_PASSWORD` | login for the dashboards and dev pages |
| `S3_ENDPOINT` | where **the app** reaches the object storage, from inside the network |
| `S3_PUBLIC_BASE_URL` | where **the browser** downloads files from, bucket name included — a file's URL is this plus `/` plus its object key |
| `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` / `S3_BUCKET` / `S3_USE_SSL` | the rest of the storage credentials (`S3_BUCKET` defaults to `uploads`; set `S3_USE_SSL=true` only when the endpoint is https) |
| `DB_POOL_MAX` | database connections **one copy** may open (default 10) — see below |
| `TRUSTED_PROXY_HOPS` | how many reverse proxies stand in front of the app (default 1) — see below |

Two of those matter as soon as you run more than one copy of the app:

- **`DB_POOL_MAX`** is per copy, and they all share one database. The rule:
  replicas x `DB_POOL_MAX` must stay below the Postgres `max_connections` limit
  (default 100), with room left for migrations, `psql` and the dashboards.
- **`TRUSTED_PROXY_HOPS`** decides which address the rate limiter counts against.
  The address is read that many entries from the **right** of `X-Forwarded-For`,
  because proxies append to that header and everything further left is whatever
  the caller typed. Set it to the number of proxies you really run (`0` = none, and
  then the forwarding headers are ignored). Guess too high and a caller can pad the
  header to choose its own address; too low and every client shares one bucket.

The meta-repo's `docs/DEPLOY.md` walks through both, plus the network effects, in
"Running more than one copy".

## The API

- **GraphQL:** `POST /graphql`. Try it in the playground at `/playground`.
  Live updates use a WebSocket on the same URL (`graphql-transport-ws`).
- **REST docs:** Swagger UI at `/swagger` (the spec file is at `/openapi.yaml`).
- **Jobs dashboard:** Asynqmon (its own container, port `:5300`).

## Finding your way around the code

### The layout

```
cmd/         entry points: server
internal/
  app/        wires everything together (used by main and by tests)
  config/     reads settings from the environment
  server/     the chi router + middleware
  middleware/ request id, recovery, real IP, rate limit, security headers
  auth/       OIDC/JWT checking, login middleware, roles, mock mode
  db/         Postgres pool, migrations, generated SQL (sqlc)
  redis/      Redis client, cache, pub/sub
  profile/    the profile service (cache + live-update events)
  upload/     file uploads to S3-compatible object storage (minio-go)
  queue/      background jobs (asynq)
  graph/      GraphQL handler, resolvers, error formatting, logging
  i18n/       translations (en/ru)
  health/     the /livez (alive) and /readyz (ready for traffic) probes
  devtools/   the /dev page, Swagger UI, OpenAPI spec
db/
  migrations/ database migrations (goose)
  queries/    SQL the code generator turns into Go (sqlc)
Caddyfile     password proxy for the dashboards
docker-compose.yml
```

Good rule of thumb: start at `internal/app/app.go` (it wires everything) and
follow the call into the package you care about.

### Debugging & reading logs

Logs are structured JSON; every request-scoped line carries a `request_id` (and
`user_id` once authenticated) so you can reconstruct a request end to end. For
how to read them and a symptom → cause → fix table, see
[docs/DEBUGGING.md](docs/DEBUGGING.md).

## Adding your own code

A quick reference (the [walkthrough](#your-first-change-a-walkthrough) shows the
full loop):

- **A GraphQL field:** edit `internal/graph/schema.graphqls` (with a description),
  run `task gen`, then write the new resolver in `internal/graph/resolver/`.
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

Unit tests use in-memory fakes, so they're fast and need no Docker — write these
for your business logic. Integration tests cover the GraphQL contract (queries,
mutations, the `profileUpdated` subscription), uploads, jobs, translations, and
health against real Postgres + Redis.

## Deploying

The production image is a tiny [distroless](https://github.com/GoogleContainerTools/distroless)
build — no shell, runs as a non-root user. All configuration comes from the
container's environment, so the same image runs in every environment.

Migrations run automatically on startup (with retries), under a Postgres advisory
lock — several copies booting at once against an empty database queue up behind one
another instead of racing. The container reports health via `server -healthcheck`,
which probes `/livez`; point your reverse proxy at `/readyz` instead, so a copy with
a sick dependency is skipped rather than restarted.

`docker-compose.prod.yml` runs the app alone — Postgres, Redis and the object
storage are external services you point `DATABASE_*`, `REDIS_*` and `S3_*` at. It
publishes no host port; it joins the reverse proxy's existing network
(`PROXY_NETWORK`, default `dokploy-network`), which is what lets you run
`docker compose -f docker-compose.prod.yml up -d --scale app=3`.

Full guide, including what to check before raising that number: the meta-repo's
`docs/DEPLOY.md`.

## License

MIT — see [LICENSE](LICENSE).
