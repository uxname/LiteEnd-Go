# AGENTS.md — guidance for AI agents & contributors

This is the Go port of the LiteEnd backend. Read this before changing code.

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
   regenerated output. CI fails if generated code is stale.
4. **Lint and format before committing:** `task fmt && task lint`. Code must pass
   `golangci-lint` (gofumpt formatting is enforced).
5. **Migrations are forward-only and embedded.** Add a new file under
   `db/migrations/` (goose format, `NNNNN_name.sql`). They run programmatically at
   startup — don't rely on a goose CLI in production.

## Architecture conventions

- **Composition root:** `internal/app/Build` wires everything. Both `cmd/server`
  and the integration tests use it, so they exercise identical wiring. Add new
  dependencies there.
- **No DI framework.** Dependencies are explicit constructor args. Define narrow
  interfaces at the consumer (e.g. `profile.Querier`, `profile.Cache`,
  `resolver.ProfileService`) to keep packages testable.
- **Auth:** the authenticated user lives in `context.Context`
  (`auth.WithUser` / `auth.UserFromContext`). Enforce access in resolvers with
  `auth.Require(ctx)` / `auth.RequireRole(ctx, role)`. Roles come from the DB
  profile, not the token.
- **Errors:** return wrapped errors (`fmt.Errorf("...: %w", err)`). GraphQL errors
  are shaped by `internal/graph/errors.go` (adds `code`, `statusCode`, `requestId`).
- **Logging:** use the injected `*slog.Logger`. Sensitive keys
  (`password`, `token`, `authorization`, …) are auto-redacted — don't log raw
  secrets anyway.

## How to add things

- **A GraphQL field:** edit `schema.graphqls` → `task gen:gqlgen` → implement the
  new resolver stub in `internal/graph/resolver/`.
- **A DB query:** add it to `db/queries/*.sql` with a `-- name:` annotation →
  `task gen:sqlc` → use `database.Queries.<Name>`.
- **A new enum/array column:** add a migration; if it's an enum, register its type
  in `internal/db/enums.go` so pgx can decode arrays.
- **A background job:** define a task type + handler in `internal/queue`, register
  it in the worker mux.
- **A translation:** add the key to both `internal/i18n/locales/en.json` and
  `ru.json` (go-i18n format, `{{.placeholder}}`).

## Testing

- **Unit tests** (no build tag) use in-memory fakes — fast, no Docker. Run with
  `task test`.
- **Integration/e2e tests** live in `test/` behind the `//go:build integration`
  tag and use **testcontainers-go** (real Postgres + Redis). Run with
  `task test:integration` (needs Docker).
- New features need tests covering the success path and the key failure modes
  (auth/role denial, validation, path-traversal, dedup, cache invalidation).

## Admin dashboards & data

- **Auth is mandatory on every admin surface.** External dashboards (pgweb,
  RedisInsight, Asynqmon) are exposed *only* through the Caddy Basic-Auth proxy
  (`admin_proxy` in compose, `Caddyfile`) — never publish their container ports
  directly. The app's dev pages (`/dev`, `/playground`, `/swagger`,
  `/openapi.json`) are wrapped with `middleware.BasicAuth` using `ADMIN_USER` /
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
- Don't add heavyweight frameworks; this template values a small, idiomatic stack.
- Don't commit secrets — `gitleaks` runs in pre-commit and CI.
