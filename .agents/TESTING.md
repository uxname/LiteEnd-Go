# Testing & TDD discipline

**Tests are not optional here, and the rule is machine-enforced** — the same force
as the frontend's trio rule.

## The discipline

New business logic — a resolver, service, job or middleware — is written
**test-first**: add the unit test that encodes the behaviour (success path + key
failure modes), watch it fail, then implement to green.

`task test:cov` enforces **per-package coverage floors** from the `override` block in
`.testcoverage.yml`, plus a total floor. New logic added to a domain package without
tests drops that package under its floor and **fails the gate** — it cannot hide in
the aggregate total.

**`.testcoverage.yml` is the source of truth for the numbers — read it, don't trust a
number quoted in prose (including here).** Two things about it:

- `override.path` is the **module-relative** package path (no module prefix), unlike
  `exclude.paths` which match the full import path. Get this wrong and the override
  silently does nothing.
- Ratchet floors **up** as coverage grows. **Never lower one to dodge a finding** —
  add the missing test. If a floor blocks you, that is the gate working.

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
- Some packages (`queue`, `redis`, `db`) need a live server and are covered by the
  integration suite rather than unit tests — don't duplicate that with mocks.

## Coverage

`task test:cov` runs every test with cross-package coverage and enforces
`.testcoverage.yml`. It runs on **pre-push**. `task test:all` is the same tests
without the coverage gate.

There is **no CI** — this gate lives entirely in the git hook, so `--no-verify`
bypasses it locally. Don't.

## Known blind spot

`internal/auth/verifier.go`'s `Verify` — the actual token check (signature, issuer,
audience, expiry) — is **untested**: every unit test builds `NewMiddleware(nil, …)`
with `mockEnabled=true`, and the integration suite sets `OIDC_MOCK_ENABLED=true`.
`Middleware.verifier` is a concrete `*Verifier`; extracting a consumer-side
interface (as `Profiles` in the same file already is) is what unblocks testing it.
Don't add code that leans on `Verify` being covered — it isn't.
