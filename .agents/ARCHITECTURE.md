# Architecture & how to add things

Read this before adding a package, a resolver, a query or a route.

## Composition root

`internal/app/Build` wires everything. Both `cmd/server` and the integration tests
use it, so they exercise identical wiring — add new dependencies there. Route
topology lives in `app.mountRoutes`, a single source of truth that the OpenAPI
route-sync test checks.

**No DI framework.** Dependencies are explicit constructor args. Define narrow
interfaces **at the consumer** (`profile.Querier`, `profile.Cache`, `auth.Profiles`,
`resolver.ProfileService`) so packages stay testable with small fakes.

## Layering

Enforced by two gates, both inside `task check`:

- **go-arch-lint** (`.go-arch-lint.yml` — the source of truth) builds the full
  component graph, catches cross-package method-call/DI edges (deepScan) and import
  cycles. Components: `entrypoint → composition → transport → domain →
  infrastructure`, plus cross-cutting commons (`config`, `logger`, `version`,
  `httperr`).
- **depguard** (`.golangci.yml`) forbids domain/infra packages (`profile`, `upload`,
  `queue`, `auth`, `redis`, `db`, `i18n`, `health`, `backup`, `middleware`, `logger`,
  `config`) from importing the transport layer (`internal/graph`, `internal/server`,
  `internal/app`).

When you add an `internal/*` package, place it in the right component in
`.go-arch-lint.yml`. **Never widen a layer's `mayDependOn` to make a
wrong-direction import compile.**

> **Known gap, don't mistake green for clean.** The current rules allow
> `transport → infrastructure` and `domain → infrastructure`, and `sqlc.Profile`
> serves as the domain model, leaking into `resolver.ProfileService`. So a resolver
> *can* run SQL past the domain and the gate stays green. Tightening the rule
> requires introducing a real domain model plus mapping first — recorded as a P2 item
> in the meta-repo's latest `docs/audits/*/audit-report.md`.

## Auth

The authenticated user lives in `context.Context` (`auth.WithUser` /
`auth.UserFromContext`). Enforce access in resolvers with `auth.Require(ctx)` /
`auth.RequireRole(ctx, role)`. **Roles come from the DB profile, not the token.**

## Errors

- Wrap errors from other packages with context (`fmt.Errorf("…: %w", err)`) —
  `wrapcheck` requires wrapping third-party errors.
- Use sentinel errors for expected outcomes (`profile.ErrProfileNotFound`,
  `upload.ErrDisallowedMime`) instead of returning `nil, nil`.
- GraphQL errors are shaped by `internal/graph/errors.go` (adds `code`,
  `statusCode`, `requestId`). REST errors go through `internal/httperr` — that is
  the single envelope, don't hand-roll `http.Error`.

## Logging

In request scope log via `logger.From(ctx)` (carries `request_id` + `user_id`, set
by `middleware.ContextLogger` and `auth`). For lifecycle/background code use the
injected `*slog.Logger`. Never the global one — `sloglint` forbids `slog.Info` /
`slog.Default` outside `cmd/`. Sensitive keys (`password`, `token`,
`authorization`, …) are auto-redacted, but still don't log raw secrets.

To read logs and triage failures: [../docs/DEBUGGING.md](../docs/DEBUGGING.md).

## How to add things

- **A GraphQL field** → edit `internal/graph/schema.graphqls` (give every new type,
  field, enum value and input a `"description"` — the schema is self-documenting and
  the descriptions surface in the playground and to agents) → `task gen` →
  implement the resolver stub in `internal/graph/resolver/`.
- **A DB query** → add it to `db/queries/*.sql` with a `-- name:` annotation →
  `task gen` → use `database.Queries.<Name>`.
- **A new enum/array column** → add a migration; if it's an enum, register its type
  in `internal/db/enums.go` so pgx can decode arrays.
- **A background job** → define a task type + handler in `internal/queue`, register
  it in the worker mux. Handlers must respect `ctx` (no uncancellable sleeps).
- **A REST route** → add it in `mountRoutes` (`internal/app/app.go`) **and** document
  it in `internal/devtools/openapi.yaml`. `TestOpenAPISpecMatchesRoutes` fails if the
  route and the spec drift apart (or methods mismatch); a non-REST/dev route goes in
  that test's `devRoutes` allowlist.
- **A translation** → add the key to both `internal/i18n/locales/en.json` and
  `ru.json` (go-i18n format, `{{.placeholder}}`).
- **A migration** → `task migration:create name=…` (goose format under
  `db/migrations/`). Forward-only and embedded: they run programmatically at
  startup, so don't rely on a goose CLI in production.

## Code navigation (CodeGraph)

Code-intelligence indexing (`codegraph_*` MCP tools — who calls what, where a symbol
is defined, impact analysis) lives **only in the LiteStack meta-repo**, which owns a
single index spanning both submodules. This sub-project carries no CodeGraph config
and no `task` for it; run queries from the meta-repo root.
