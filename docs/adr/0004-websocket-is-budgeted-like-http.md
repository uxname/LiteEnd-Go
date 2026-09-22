# ADR 0004: A WebSocket is budgeted like HTTP

- **Date:** 2026-09-23
- **Status:** accepted

## Context

Every limit this backend had was an HTTP middleware: the per-IP rate limit, the body
cap, the server's read/write/idle timeouts. A GraphQL WebSocket passes all of them
exactly once — at the upgrade — and then lives outside them. The security audit of
2026-09-22 found what that meant in practice:

- **Operations were unmetered.** One socket ran any number of queries and mutations
  after the single rate-limit unit its upgrade cost, including after the same address
  was already being answered 429 over HTTP.
- **Sockets were unbounded in time.** `net/http` drops its deadlines on hijack, only
  the legacy `graphql-ws` subprotocol had a keep-alive, and `InitFunc` accepted
  anonymous sockets — so a client could hold sockets (and their goroutines, buffers
  and file descriptors) for as long as it kept TCP alive, without credentials.
- **Subscriptions cost Redis connections.** Each `profileUpdated` subscription opened
  its own Redis subscription, i.e. a dedicated connection outside the pool limits. One
  client could exhaust the Redis that also holds the role cache, the rate limiter and
  the job queue for everyone.

The frontend does not use WebSockets today, but the transport is part of the template's
public API, and derived products will.

## Decision

**1. No anonymous sockets.** `connection_init` without valid credentials closes the
socket with 4403. Every operation over a socket needs a user anyway (the one
subscription requires auth; anonymous queries have HTTP), so an anonymous socket is
only ever a held resource.

**2. A socket is bounded in time.** `connection_init` must arrive within 10 s; an
initialised `graphql-transport-ws` socket is pinged every 25 s and closed after two
missed pongs; a socket opened with a bearer token closes when that token expires (plus
the usual clock skew). Frames are capped at 128 KiB.

**3. Operations draw from the HTTP budget.** The handler remembers the upgrade
request's rate key (`rl:auth:<ip>`) and charges every operation sent over the socket to
it through `AroundOperations`. HTTP operations are not charged again. Per-user quotas
(`addTestJob`) are charged in their resolvers, so they count on both transports.

**4. Subscriptions fan out in-process.** Each process holds one Redis subscription
(`PSUBSCRIBE profile:updated:*`) and dispatches events to local subscribers by profile
id; a socket may hold 10 live subscriptions. Redis connections scale with the number of
app copies, not with client input.

## Alternatives

- **A separate WebSocket budget (per user).** Users behind one NAT would not share it,
  but it is a second rule to reason about, and a shared budget is what makes "the API
  allows N operations a minute" true regardless of transport.
- **Rely on the reverse proxy's idle timeout.** Not visible from this repository and
  different per deployment; the app should not depend on it for its own liveness.
- **Only cap subscriptions per socket.** Bounds one socket, not many: sockets are cheap
  to open (one rate-limit unit each), so Redis connections would still follow the
  client's choice.

## Consequences

- A WebSocket client must authenticate in `connection_init` and answer pings; one that
  stays silent is closed.
- A burst over a socket now meets the same 100/min budget as HTTP and gets
  `TOO_MANY_REQUESTS` errors instead of executing.
- Profile events for a subscriber whose 1-event buffer is still full are dropped rather
  than waited for: an event is the profile's latest state, and one slow reader must not
  stall delivery to the rest.
