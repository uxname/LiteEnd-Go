# ADR 0001: Go for the backend

- **Date:** 2026-09-04
- **Status:** accepted

## Context

This backend is written to be developed largely **by AI agents**, and the failure mode
that costs the most in that setting is not a wrong algorithm — it is code that looks
finished, passes review by eye, and is broken in a way nothing catches until runtime. An
agent has no intuition about a codebase it met ten minutes ago; the only cheap check it
has is a machine that says *no*.

There is no CI here (see the meta-repo's `ADR-0001`): every guarantee has to come from the
local gate, and the earlier in that gate an error surfaces, the cheaper it is.

## Decision

The backend is written in Go — compiled and strictly typed, on a small idiomatic stack
(chi, gqlgen, sqlc, goose).

## Alternatives

- **TypeScript / Node** — the obvious pairing with the frontend, one language across both
  sides. Rejected as the primary reason for this ADR: `any` is always available, and a
  third-party package with weak or hand-written types re-opens the hole even in a strict
  project. The type system is a convention there; here it is the compiler.
- **Python** — fastest to write, weakest static guarantee: the same class of runtime
  surprises, with less tooling to catch them ahead of time.
- **Rust** — stronger guarantees than Go, at a cost in compile times, ceremony and
  ecosystem breadth that a boilerplate meant for quick product starts should not charge.

## Consequences

- **The compiler is the first gate.** Broken code does not reach a commit, let alone a
  run — a whole class of mechanical failures never becomes an agent's problem, which
  leaves logic and business rules as the things worth reviewing.
- **Strict typing comes from the ecosystem, not from discipline.** There is no `any` to
  reach for and no need to audit whether a dependency ships honest types.
- **Fast**: startup and throughput are good enough that performance work is rarely the
  reason a change is needed.
- **A broad ecosystem**, though smaller than Node's for niche integrations — occasionally
  a library that exists on npm has to be written here instead.
- The price: more verbosity than TypeScript, two languages in one product (so a
  frontend-only contributor cannot follow the backend without learning Go), and a
  generated-code layer (sqlc, gqlgen) that must be regenerated rather than edited — see
  `AGENTS.md`, golden rule 2.
