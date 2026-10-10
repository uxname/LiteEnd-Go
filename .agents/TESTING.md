# Testing & TDD discipline

**Tests are machine-enforced**: the pre-push hook runs `task test:cov`, and a package
(or the total) below its coverage floor fails the push.

## The discipline

New business logic is written **test-first**:
[CODING_STANDARDS.md → Tests and coverage floors](./CODING_STANDARDS.md#tests-and-coverage-floors).

`task test:cov` enforces **per-package coverage floors** from the `override` block in
`.testcoverage.yml`, plus a total floor. New logic added to a domain package without
tests drops that package under its floor and **fails the gate** — it cannot hide in
the aggregate total.

**`.testcoverage.yml` is the source of truth for the numbers; read them there.** Two
things about it:

- **Both** `override.path` and `exclude.paths` are **module-relative** regexps
  (`^internal/...`, no module prefix); a full import path matches nothing and the entry
  silently does nothing. After editing either list, run `task test:cov` and confirm the
  reported total (or that package's figure) moved.
- How floors move (only up, in the same change): [CODING_STANDARDS.md](./CODING_STANDARDS.md#tests-and-coverage-floors).

## What new code must cover

The success path **and** the key failure modes: auth/role denial, validation
boundaries, path traversal, dedup, cache invalidation. For anything that decides
whether data is destroyed or a request is authorised, cover the negative case
explicitly.

## Unit vs integration

- **Unit tests** (no build tag) use in-memory fakes — fast, no Docker. Run with
  `task test` (race detector on). Put `t.Parallel()` at the top of each unit test
  (the linter enforces it); the exception is tests that call `t.Setenv`.
- **Integration/e2e tests** live in `test/` behind the `//go:build integration` tag
  and use **testcontainers-go** (real Postgres + Redis). They run sequentially
  (shared DB). Run with `task test:integration` (needs Docker).
- `queue`, `redis` and `db` need a live server: the integration suite covers them
  against real Postgres and Redis.

## Coverage

`task test:cov` runs every test with cross-package coverage and enforces
`.testcoverage.yml`. `task test:all` is the same tests without the coverage gate.

## The real token check

`internal/auth/verifier.go`'s `Verify` — signature, issuer, audience, expiry — is
covered by `verifier_test.go` with **no mock auth**: the test starts a local JWKS
endpoint (`httptest`), signs its own tokens with the matching key, and asserts that
an expired, foreign-issuer, foreign-audience or foreign-signature token is refused.
The middleware tests still build `NewMiddleware(nil, …)` with `mockEnabled=true` —
that is their subject, not a gap.

`config.OIDCClockSkew` is the tolerated clock drift between this host and the
issuer, and it is tested from both sides: expired inside the tolerance passes,
expired past it does not. Change the constant and that test tells you.
