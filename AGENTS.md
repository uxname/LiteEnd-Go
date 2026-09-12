# AGENTS.md — liteend-go (backend)

The Go port of the LiteEnd backend. **This file is the entry point, not the whole
manual:** it holds the rules you must not break and a map of where everything else
lives. Read the file that matches your task — don't read them all.

> Commands are `task <name>`. On Arch Linux the runner is the `go-task` package
> (`go-task <name>`). All dev tools are pinned in the `tool` block of `go.mod` and
> invoked with `go tool <path>` — no separate `go install` needed.
> A tool version lives in that block and nowhere else — the Taskfile pins nothing.

## Where to look

| Your task | Read |
|---|---|
| Add a resolver, query, migration, job, REST route, translation | [.agents/ARCHITECTURE.md](./.agents/ARCHITECTURE.md) |
| Understand layering / where a new package belongs | [.agents/ARCHITECTURE.md](./.agents/ARCHITECTURE.md) |
| Write tests, hit a coverage floor, pick unit vs integration | [.agents/TESTING.md](./.agents/TESTING.md) |
| A gate is failing, or you're about to claim "done" | [.agents/QUALITY-GATES.md](./.agents/QUALITY-GATES.md) |
| Env vars, admin dashboards, volumes, deploy | [.agents/OPERATIONS.md](./.agents/OPERATIONS.md) |
| Read logs, triage a failure | [docs/DEBUGGING.md](./docs/DEBUGGING.md) |
| **Why** this backend is the way it is — language, layering, deliberate omissions | [docs/adr/](./docs/adr/) |
| How this side pairs with the frontend | meta-repo `AGENTS.md` |
| The architecture diagram (LikeC4) | it lives in the LiteStack meta-repo (`docs/architecture/likec4/`) — update it there, never start a second model here |

## Golden rules

1. **Preserve the GraphQL API contract.** `internal/graph/schema.graphqls` is the
   source of truth and must stay compatible with existing frontends. Changing
   operation names, types, or the `graphql-transport-ws` subscription protocol is a
   breaking change — avoid it.
2. **Generated code is generated.** Never hand-edit `internal/db/sqlc/**` (edit
   `db/queries/*.sql`) or `internal/graph/generated/**` and
   `internal/graph/model/models_gen.go` (edit `schema.graphqls`). Resolver bodies in
   `internal/graph/resolver/*.resolvers.go` **are** hand-written and survive
   regeneration.
3. **Run `task gen` after touching SQL or the GraphQL schema, and commit the
   output.** `task gen` also formats — gqlgen output is not gofumpt-clean on its own.
4. **New logic ships with a test.** Test-first; per-package coverage floors are
   machine-enforced. Never lower a floor to go green.
5. **`task check` before committing, `task test:cov` before pushing.** There is no
   CI — these hooks are the entire guarantee, so `--no-verify` has nothing behind it.
6. **English-only in the repo.** All code, comments, identifiers, commit messages and
   docs are English. (Chat with the user in their language.)

## Don'ts

- Don't enable `OIDC_MOCK_ENABLED` in production (config rejects it).
- Don't build an object key from a client-supplied name without `objectKey`/`safeExt`
  (`internal/upload/service.go`) — an object key is a path, and that pair is the
  path-traversal guard. It must stay covered by a test.
- Don't expose an admin dashboard without the auth proxy / Basic-Auth.
- Don't use the global `slog` logger in `internal/` — inject `*slog.Logger`, or use
  `logger.From(ctx)` in request scope.
- Don't log a failure at `INFO`. `level=ERROR` must select exactly what is *our*
  fault; a client fault is `WARN`. See [.agents/ARCHITECTURE.md](./.agents/ARCHITECTURE.md#logging).
- Don't swallow an error path without a log line. If it is masked for the client,
  the original message goes to the log first.
- Don't let domain/infra packages import the transport layer (depguard blocks it).
- Don't widen a layer's `mayDependOn` to make an import compile.
- Don't add heavyweight frameworks; this template values a small, idiomatic stack.
- Don't commit secrets — `gitleaks` runs in `task check` (when installed).
