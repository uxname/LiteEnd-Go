# Coding standards — how code, tests, logs and commits are written

Holds for everything in this repo. Inside the LiteStack meta-repo it refines the root
`.agents/CODING_STANDARDS.md`.

## Tests and coverage floors

- **New logic is written test-first** — a resolver, service, job or middleware gets the
  test that encodes its behaviour (success path + key failure modes) before the code.
  Tests check behaviour, not lines.
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
  `*slog.Logger` elsewhere (`sloglint` rejects the global one outside `cmd/`). Keys
  like `password` and `token` are redacted, but raw secrets stay out of messages too.
- **The level is the severity, not the location.** `ERROR` selects exactly what is our
  fault — 5xx responses, internal GraphQL errors, failed jobs and queries. A client
  fault (4xx, `FORBIDDEN`, `BAD_USER_INPUT`) is `WARN`. Routine traffic is `INFO`.
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
