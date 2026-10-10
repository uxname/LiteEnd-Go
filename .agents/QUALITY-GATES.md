# Quality gates — what blocks a commit and a push

This project has **no CI service**: it is meant to deploy in any environment, so the
gates live in the `Taskfile` and run locally via git hooks (lefthook). Install them
with `task setup` (or `lefthook install`). The hooks call `task` / `go-task`
(whichever is on PATH), so the exact same checks run by hand and in the hook.

Because the hooks *are* the guarantee, `--no-verify` has no safety net behind it.
Don't use it.

> Note: the backend's hooks are installed by `task setup`. A clone where someone only
> compiled the project has **no** gates at all.

## `task check` — pre-commit

Run it by hand before committing if you want to fail fast. It fails on:

1. **Stale generated code** (`task gen:check` — sqlc/gqlgen out of sync).
2. **`go.mod`/`go.sum` not tidy** (`task tidy:check`).
3. **Compose files that do not render** (`task compose:check` — schema, interpolation,
   `:?` guards; skipped with a warning if docker is absent).
4. **Lint** issues (`golangci-lint`, includes `gci` import ordering).
5. **Architecture** violations (`task arch` — go-arch-lint: layering, cross-package
   call edges, import cycles).
6. **Dead code** anywhere in the program (`task deadcode`).
7. **Secrets** (`gitleaks`, **skipped with a warning if gitleaks is absent** — a green
   run on a machine without it proves nothing).

> `gen:check` regenerates and then diffs the **working tree**, so a legitimate
> regeneration reads as "stale" until you `git add` it. That is why a codegen-tool
> bump needs `task gen` → `git add` → `task check`, in that order.

## pre-push — `task test:cov`, `task vuln`, `task secrets:history`

Every test (unit + integration via testcontainers) plus the coverage-threshold gate.
Needs Docker. Details: [TESTING.md](./TESTING.md). Plus `govulncheck` (`task vuln`):
it is the one gate that needs the network and the one whose verdict can change
without the code changing, so it runs on push rather than on every commit. And
`task secrets:history` — gitleaks over the whole history, which is the only place a
merge commit or a `--no-verify` commit can still be caught.

## Linter rules worth knowing

`task lint` runs `golangci-lint` in a strict configuration (`.golangci.yml`). Keep it
at **zero issues**.

- **`//nolint` needs a reason.** `nolintlint` requires `//nolint:<linter> // why`; a
  bare `//nolint` fails, and so does a suppression that isn't actually suppressing
  anything. Only suppress a genuine false positive or an intentional, documented
  exception (e.g. the `version.*` vars are globals on purpose — injected via
  `-ldflags`).
- **Complexity gates** are on (`funlen`, `gocognit`). If a
  function trips them, **split it** — don't raise the threshold.
- **Formatting** is `gofumpt` + `gci` import ordering (stdlib → third-party →
  `github.com/uxname/liteend-go`). `task fmt` applies both; `task lint` verifies them as part of the lint run.

## Definition of done

It lives in [`AGENTS.md`](../AGENTS.md) → "Every task", step 4, with "Move the
code, never the gate" under Guardrails — both are read on every task.
