# AGENTS.md — liteend-go (backend)

The Go port of the LiteEnd backend; it also runs standalone. Inside the LiteStack
meta-repo, the root `AGENTS.md` adds what spans both sides.

Commands are `task <name>` (`go-task <name>` on Arch Linux). Dev tools run as
`go tool <path>` from the `tool` block of `go.mod` — nothing to install.

## Every task

1. **Route.** Read the guide the table below names for your task before writing code.
2. **Read the ADRs** in [docs/adr/](./docs/adr/) that cover the area you change. A
   structural decision gets a new ADR in the same change — when and how:
   [docs/adr/README.md](./docs/adr/README.md).
3. **Write to the standard.** Code, tests, log lines and commit messages follow
   [.agents/CODING_STANDARDS.md](./.agents/CODING_STANDARDS.md). Everything committed is
   English, whatever language the chat is in.
4. **Done means green by exit status**, never by the tail of a pipeline: `task check`
   and `task test:cov` both exit 0. Without Docker, run `task test` and say that
   integration and coverage were skipped. A skipped step or a red test is reported with
   its output.

## Where to look

| Your task | Read |
|---|---|
| **Add** a resolver, query, migration, job, REST route, translation; **place** a new package in the layers | [.agents/ARCHITECTURE.md](./.agents/ARCHITECTURE.md) |
| **Tests**: unit vs integration, what new code must cover, a coverage floor blocks you | [.agents/TESTING.md](./.agents/TESTING.md) |
| A **gate** is failing | [.agents/QUALITY-GATES.md](./.agents/QUALITY-GATES.md) |
| **Env** vars, admin **dashboards**, volumes, **deploy** | [.agents/OPERATIONS.md](./.agents/OPERATIONS.md) |
| **Triage** a failure from the logs | [docs/DEBUGGING.md](./docs/DEBUGGING.md) |
| A **seam** with the frontend (schema codegen, auth audience, CORS + WebSockets, `requestId`) | inside LiteStack: the meta-repo's `.agents/CROSS-PROJECT.md` |
| A change to boundaries, protocols or components | the **architecture model** lives in the LiteStack meta-repo (`docs/architecture/likec4/`) — it moves there, in the same change |

## Guardrails

- **The GraphQL contract holds.** `internal/graph/schema.graphqls` is the source of
  truth and must stay compatible with existing frontends: add fields and types; keep
  operation names, types and the `graphql-transport-ws` subscription protocol as they are.
- **Edit the source, then run `task gen` and commit its output.** SQL lives in
  `db/queries/*.sql`, the API in `schema.graphqls`; `internal/db/sqlc/**`,
  `internal/graph/generated/**` and `internal/graph/model/models_gen.go` are output.
  Resolver bodies in `internal/graph/resolver/*.resolvers.go` are hand-written and
  survive regeneration.
- **Move the code, never the gate.** A red gate is fixed with a test, a split function
  or a moved package. Coverage floors, `funlen`/`gocognit` thresholds and a layer's
  `mayDependOn` in `.go-arch-lint.yml` stay where they are; a new `internal/*` package
  gets its place in that file.
- **Admin surfaces sit behind auth.** A new dashboard goes behind the Basic-Auth proxy,
  like the existing ones ([.agents/OPERATIONS.md](./.agents/OPERATIONS.md)).
- **Object keys are built by the server** (`objectKey` in `internal/upload/service.go`);
  nothing the client sends becomes part of a storage path.
- **The hooks are the whole guarantee.** There is no CI, so every commit and push runs
  them; `--no-verify` skips all of it.
