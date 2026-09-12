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
3. **Lint** issues (`golangci-lint`, includes `gci` import ordering).
4. **Architecture** violations (`task arch` — go-arch-lint: layering, cross-package
   call edges, import cycles).
5. **Dead code** anywhere in the program (`task deadcode`).
6. **Vulnerabilities** (`govulncheck`).
7. **Secrets** (`gitleaks`, **skipped with a warning if gitleaks is absent** — a green
   run on a machine without it proves nothing).

> `gen:check` regenerates and then diffs the **working tree**, so a legitimate
> regeneration reads as "stale" until you `git add` it. That is why a codegen-tool
> bump needs `task gen` → `git add` → `task check`, in that order.

## `task test:cov` — pre-push

Every test (unit + integration via testcontainers) plus the coverage-threshold gate.
Needs Docker. Details: [TESTING.md](./TESTING.md).

## Linter rules worth knowing

`task lint` runs `golangci-lint` in a strict configuration (`.golangci.yml`). Keep it
at **zero issues**.

- **`//nolint` needs a reason.** `nolintlint` requires `//nolint:<linter> // why`; a
  bare `//nolint` fails, and so does a suppression that isn't actually suppressing
  anything. Only suppress a genuine false positive or an intentional, documented
  exception (e.g. the `version.*` vars are globals on purpose — injected via
  `-ldflags`).
- **Complexity gates** are on (`cyclop`, `funlen`, `gocognit`, `nestif`). If a
  function trips them, **split it** — don't raise the threshold.
- **Formatting** is `gofumpt` + `gci` import ordering (stdlib → third-party →
  `github.com/uxname/liteend-go`). `task fmt` applies both; `task lint` verifies them as part of the lint run.

## Definition of done — read before claiming a change is finished

A change is done only when ALL of these hold. Do not report success otherwise:

1. **`task check` passes** (the full gate above).
2. **`task test:cov` passes** (unit + integration + coverage floors; needs Docker).
   If Docker is unavailable, run `task test` and **say** that integration and
   coverage were skipped.
3. **New behaviour has a test** that fails without the change. Don't lower coverage.
4. **New packages are placed in the layer graph** (`.go-arch-lint.yml`).
5. **No new `//nolint` without a reason**, and no raised complexity or coverage
   thresholds to dodge a finding — split the function or add the test.
6. **Report honestly.** If a step was skipped or a test failed, say so with the
   output. Never claim green without having run the gate — and check the command's
   exit status, not the tail of a pipeline (a `… | tail` hides a non-zero exit).
