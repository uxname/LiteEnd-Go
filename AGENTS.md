# AGENTS.md — guidance for AI agents & contributors

This is the Go port of the LiteEnd backend. Read this before changing code.

> Commands use `task <name>`. On Arch Linux the runner is the `go-task` package
> (`go-task <name>`). All dev tools are pinned in the `tool` block of `go.mod`
> and invoked with `go tool <path>` — no separate `go install` needed.

## Golden rules

1. **Preserve the GraphQL API contract.** `internal/graph/schema.graphqls` is the
   source of truth and must stay compatible with existing frontends. Changing
   operation names, types, or the `graphql-transport-ws` subscription protocol is
   a breaking change — avoid it.
2. **Generated code is generated.** Never hand-edit:
   - `internal/db/sqlc/**` → edit `db/queries/*.sql`, then `task gen:sqlc`.
   - `internal/graph/generated/**`, `internal/graph/model/models_gen.go` → edit
     `internal/graph/schema.graphqls`, then `task gen:gqlgen`.
   Resolver bodies in `internal/graph/resolver/*.resolvers.go` ARE hand-written
   and preserved across regeneration.
3. **Run `task gen` after touching SQL or the GraphQL schema**, and commit the
   regenerated output. `task gen` also formats — gqlgen output is not
   gofumpt-clean on its own. `task check` fails if generated code is stale
   (`task gen:check`).
4. **Format and lint before committing:** `task fmt && task lint` (or just
   `task check`). Code must pass `golangci-lint` with zero issues. The
   `pre-commit` hook (lefthook) runs `task check` to enforce this.
5. **Migrations are forward-only and embedded.** Create one with
   `task migration:create name=...` (goose format under `db/migrations/`). They
   run programmatically at startup — don't rely on a goose CLI in production.

## Architecture conventions

- **Composition root:** `internal/app/Build` wires everything. Both `cmd/server`
  and the integration tests use it, so they exercise identical wiring. Add new
  dependencies there. Route topology lives in `app.mountRoutes` (a single source
  of truth that the OpenAPI route-sync test checks).
- **No DI framework.** Dependencies are explicit constructor args. Define narrow
  interfaces at the consumer (e.g. `profile.Querier`, `profile.Cache`,
  `auth.Profiles`, `resolver.ProfileService`) to keep packages testable.
- **Layering (enforced by depguard).** Dependencies point inward: domain/infra
  packages (`profile`, `upload`, `queue`, `auth`, `redis`, `db`, `i18n`,
  `health`, `backup`, `middleware`, `logger`, `config`) must NOT import the
  transport layer (`internal/graph`, `internal/server`, `internal/app`). `task
  lint` fails if they do.
- **Auth:** the authenticated user lives in `context.Context`
  (`auth.WithUser` / `auth.UserFromContext`). Enforce access in resolvers with
  `auth.Require(ctx)` / `auth.RequireRole(ctx, role)`. Roles come from the DB
  profile, not the token.
- **Errors:** wrap errors from other packages with context
  (`fmt.Errorf("...: %w", err)`) — `wrapcheck` requires wrapping third-party
  errors. Use sentinel errors for expected outcomes (e.g.
  `profile.ErrProfileNotFound`, `upload.ErrDisallowedMime`) instead of returning
  `nil, nil`. GraphQL errors are shaped by `internal/graph/errors.go` (adds
  `code`, `statusCode`, `requestId`).
- **Logging:** use the injected `*slog.Logger`, never the global one
  (`sloglint` forbids `slog.Info`/`slog.Default` outside `cmd/`). Sensitive keys
  (`password`, `token`, `authorization`, …) are auto-redacted — still don't log
  raw secrets.

## How to add things

- **A GraphQL field:** edit `schema.graphqls` → `task gen` → implement the new
  resolver stub in `internal/graph/resolver/`.
- **A DB query:** add it to `db/queries/*.sql` with a `-- name:` annotation →
  `task gen` → use `database.Queries.<Name>`.
- **A new enum/array column:** add a migration; if it's an enum, register its type
  in `internal/db/enums.go` so pgx can decode arrays.
- **A background job:** define a task type + handler in `internal/queue`, register
  it in the worker mux.
- **A REST route:** add it in `mountRoutes` (`internal/app/app.go`) AND document it
  in `internal/devtools/openapi.yaml`. `TestOpenAPISpecMatchesRoutes` fails if the
  route and the spec drift apart (or methods mismatch); a non-REST/dev route goes
  in that test's `devRoutes` allowlist.
- **A translation:** add the key to both `internal/i18n/locales/en.json` and
  `ru.json` (go-i18n format, `{{.placeholder}}`).

## Quality & linters

`task lint` runs `golangci-lint` in a strict configuration (`.golangci.yml`).
Keep it at **zero issues**.

- **`//nolint` needs a reason.** `nolintlint` requires the form
  `//nolint:<linter> // why`. A bare `//nolint` fails. Only suppress when the
  finding is a genuine false positive or an intentional, documented exception
  (e.g. the `version.*` vars are globals on purpose — injected via `-ldflags`).
- **Complexity gates** are on (`cyclop`, `funlen`, `gocognit`, `nestif`). If a
  function trips them, split it — don't raise the threshold.
- **Formatting** is `gofumpt` + `gci` import ordering (stdlib → third-party →
  `github.com/uxname/liteend-go`). `task fmt` applies both; `task check` verifies
  it.
- **Vulnerabilities:** `task vuln` (`govulncheck`) must report none. It runs as
  part of `task check` (pre-commit).

## Testing

- **Unit tests** (no build tag) use in-memory fakes — fast, no Docker. Run with
  `task test` (race detector on). Put `t.Parallel()` at the top of each unit test
  (the linter enforces it); the exception is tests that call `t.Setenv`.
- **Integration/e2e tests** live in `test/` behind the `//go:build integration`
  tag and use **testcontainers-go** (real Postgres + Redis). They run
  sequentially (shared DB). Run with `task test:integration` (needs Docker).
- **What new code must cover:** the success path and the key failure modes
  (auth/role denial, validation, path-traversal, dedup, cache invalidation).
- **Coverage:** run `task test:cov` for a merged coverage report when you want to
  track it. There's no enforced floor (no CI) — keep new code well covered per
  the rule above.
- Some packages (`queue`, `redis`, `db`) need a live server and are covered by
  the integration suite rather than unit tests — don't duplicate that with mocks.

## Local gates (what blocks a commit / push)

This project has **no CI service** — it's meant to be deployed in any
environment, so the gates live in the `Taskfile` and run locally via git hooks
(lefthook). Two commands are the whole gate:

- **`task check`** — the full project check, runs on `pre-commit`. It fails on:
  1. **Stale generated code** (`task gen:check` — sqlc/gqlgen out of sync).
  2. **`go.mod`/`go.sum` not tidy** (`task tidy:check`).
  3. **Formatting** not gofumpt-clean (`task fmt:check`).
  4. **Build** errors (`go build ./...`).
  5. **Lint** issues (`golangci-lint`, includes `gci` import ordering).
  6. **Vulnerabilities** (`govulncheck`).
  7. **Secrets** detected (`gitleaks`, if installed).
- **`task test:all`** — every test (unit + integration via testcontainers),
  runs on `pre-push`. Needs Docker.

Install the hooks with `task setup` (or `lefthook install`). The hooks call
`task` / `go-task` (whichever is on PATH), so the exact same checks run by hand
and in the hook. Run `task check` before committing if you want to fail fast.

## Admin dashboards & data

- **Auth is mandatory on every admin surface.** External dashboards (pgweb,
  RedisInsight, Asynqmon) are exposed *only* through the Caddy Basic-Auth proxy
  (`admin_proxy` in compose, `Caddyfile`) — never publish their container ports
  directly. The app's dev pages (`/dev`, `/playground`, `/swagger`,
  `/openapi.yaml`) are wrapped with `middleware.BasicAuth` using `ADMIN_USER` /
  `ADMIN_PASSWORD`. If you add a new dashboard, put it behind the proxy too.
- Credentials: `ADMIN_USER` / `ADMIN_PASSWORD` (Go side) and `ADMIN_PASSWORD_HASH`
  (bcrypt, for Caddy — escape `$` as `$$` in `.env`). Keep all three in sync.
- **Persistent state.** `./data/uploads` and `./data/database_backups` are
  host-mounted; the `data_init` service pre-chowns them (uid 65532 / 70) for the
  non-root containers. Postgres/Redis use named volumes — do NOT bind-mount their
  data to `./data` (root-owned `0700` files there break `go test ./...`). If you
  add a writable host dir for a non-root container, extend `data_init`.

## Don'ts

- Don't enable `OIDC_MOCK_ENABLED` in production (config rejects it).
- Don't bypass `upload.SafeFileInfo` when serving files (path-traversal guard).
- Don't expose an admin dashboard without the auth proxy / Basic-Auth.
- Don't use the global `slog` logger in `internal/` — inject `*slog.Logger`.
- Don't let domain/infra packages import the transport layer (depguard blocks it).
- Don't add heavyweight frameworks; this template values a small, idiomatic stack.
- Don't commit secrets — `gitleaks` runs in `task check` (pre-commit).
