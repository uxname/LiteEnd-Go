# Coding standards — how code, tests, logs and commits are written

Holds for everything in this repo. Inside the LiteStack meta-repo it refines the root
`.agents/CODING_STANDARDS.md`.

## Tests and coverage floors

- **New logic is written test-first** — a resolver, service, job or middleware gets the
  test that encodes its behaviour (success path + key failure modes) before the code.
  Run the new test and see it go *red* on the missing behaviour (not on a compile
  error), then write code until it is green — done when you have seen both. Tests check
  behaviour, not lines.
- **Run `task test:cov` before you finish.** When the measured coverage rose, raise the
  floors in `.testcoverage.yml` to just under the new numbers in the same change — a
  point or two of headroom, no more. A floor left below the real number is a hole new
  untested code can slip through.
- A floor only goes up: when one blocks you, write the missing test. A new package
  arrives with its own floor in the `override` block.

What new code must cover, unit vs integration: [TESTING.md](./TESTING.md).

## Logs

Where each logger comes from: [ARCHITECTURE.md → Logging](./ARCHITECTURE.md#logging).

- **Log through what you were given**: `logger.From(ctx)` in request scope, the injected
  `*slog.Logger` elsewhere (`sloglint` rejects the global one outside `cmd/`).
- **Secrets stay out of the text.** An attribute whose key is in `sensitiveKeys`
  (`internal/logger/logger.go`, matched case-insensitively) is written as `[REDACTED]`;
  a message string is written as is, so keep raw secrets out of it.
- **The level is the severity, not the location.** `ERROR` selects exactly what is our
  fault — 5xx responses, internal GraphQL errors, panics, failed jobs and failed
  queries (`db_query_failed`). `WARN` is a client fault — 4xx, `UNAUTHENTICATED`,
  `FORBIDDEN`, `BAD_USER_INPUT`, a rejected token — or trouble the request survived: a
  slow query (`db_query_slow`), Redis unavailable behind the cache or the rate limiter.
  Routine traffic is `INFO`. HTTP status → level is `middleware.statusLevel`.
- **Every failure path leaves exactly one line, with the original message.** An error
  masked for the client logs its unmasked text first — see `newErrorPresenter`.
- **Every line is correlatable.** Where `request_id` cannot reach a line, put on it
  whatever identifies the work (`task_id`, `type`, `path`).

## Dependencies and tools

- The template values a small, idiomatic stack: the standard library first, then a
  dependency already in `go.mod`; a heavyweight framework needs a real reason.
- A dev tool is added with `go get -tool` and run as `go tool <path>`; its version
  lives in the `tool` block of `go.mod` and nowhere else — the Taskfile pins nothing.
- Every `//nolint` names its linter and says why: `//nolint:<linter> // why`.

## Commit messages

Conventional Commits, all lower case: `type(scope): summary` — `fix(upload):`,
`chore(build):`, `test(coverage):`. The scope is the area you touched.
