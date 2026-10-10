# Quality gates — what blocks a commit and a push

This project has **no CI service**: it is meant to deploy in any environment, so the
gates live in the `Taskfile` and run locally via git hooks (lefthook). Install them
with `task setup` (or `lefthook install`); a clone without that runs no gates at all.
The hooks call `task` / `go-task` (whichever is on PATH), so the exact same checks run
by hand and in the hook.

## `task check` — pre-commit

Run it by hand before committing. It fails on:

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

- **`//nolint` needs a reason**, in the form
  [CODING_STANDARDS.md](./CODING_STANDARDS.md#dependencies-and-tools) gives.
  `nolintlint` fails a bare `//nolint` and a suppression that suppresses nothing.
  Suppress only a genuine false positive or an intentional, documented
  exception (e.g. the `version.*` vars are globals on purpose — injected via
  `-ldflags`).
- **Complexity gates** are on (`funlen`, `gocognit`). A function that trips them is
  **split** ([AGENTS.md → Guardrails](../AGENTS.md#guardrails)).
- **Formatting** is `gofumpt` + `gci` import ordering (stdlib → third-party →
  `github.com/uxname/liteend-go`). `task fmt` applies both; `task lint` verifies them as part of the lint run.

## Definition of done

It lives in [`AGENTS.md`](../AGENTS.md) → "Every task", step 4. How a red gate is
fixed, and why `--no-verify` skips the whole guarantee: [AGENTS.md →
Guardrails](../AGENTS.md#guardrails).
