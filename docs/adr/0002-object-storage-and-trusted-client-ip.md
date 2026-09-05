# ADR 0002: Uploads live in object storage, and the client IP comes from a counted proxy chain

- **Date:** 2026-09-05
- **Status:** accepted

## Context

This backend was written to run as a single process. Making it safe to run as several
identical copies behind one proxy (see the meta-repo's `ADR-0005`) exposed two things that
were not merely single-copy assumptions.

**Uploads were local.** `internal/upload` wrote files into a directory inside the
container and served them back through `GET /uploads/*`. With more than one copy, a file
written by copy A is a 404 on copy B — and which copy a browser reaches is the proxy's
choice, not the user's.

**The client address was whatever the client typed.** `internal/middleware/realip.go`
overwrote `r.RemoteAddr` with the **leftmost** entry of `X-Forwarded-For`, and believed
`X-Real-IP` unconditionally when the first header was absent. `X-Forwarded-For` is written
by the caller, and a proxy that is configured to append adds its own entry to the right of
what arrived, so the leftmost entry is a value the caller chose whatever the topology is.
`internal/middleware/ratelimit.go` keys the rate limit on that same `RemoteAddr`, and
`internal/upload/handler.go` wrote it into the database as `uploader_ip`.

That is a **live hole in the existing code, not a risk introduced by running several
copies**: any caller could mint a fresh rate-limit bucket per request with one header, on
a single instance, today. It had a second half that made it worse — the middleware wrote
the address back **without a port**, so `net.SplitHostPort` in the rate limiter failed and
the code fell back to the raw string, making the entire forged header the bucket key.

## Decision

**1. Uploaded files are objects in an S3-compatible store**, written through `minio-go`.
Every copy writes to the same bucket, so a file stored by one is readable through all of
them. The URL kept in the database is the object's **permanent public URL**
(`S3_PUBLIC_BASE_URL` + `/` + object key), and the browser fetches it **straight from the
store**. The application serves no download route: `GET /uploads/*` is gone from
`mountRoutes`, from `internal/devtools/openapi.yaml` and from `internal/app/routes_test.go`
together. Object keys are paths, so the path-traversal guard moved with the data — it now
sanitises the extension while the key is built (`objectKey`/`safeExt` in
`internal/upload/service.go`) instead of guarding a file read that no longer happens.

**2. The client address is taken `TRUSTED_PROXY_HOPS` entries from the RIGHT** of
`X-Forwarded-For` — the entry our own proxy appended, and therefore out of the caller's
reach **as long as the proxy really appends it**. That last clause is a deployment
requirement this code cannot enforce or even detect: a proxy that forwards the header
untouched (nginx with a bare `proxy_pass` does exactly that — it sets no forwarding header
of its own) leaves the rightmost entry client-written, and the count then reads a forged
value with full confidence. The requirement, the nginx lines that satisfy it, the opposite
Caddy default, and a one-request recipe for checking a live deployment are written down in
the meta-repo's `docs/DEPLOY.md`, under "Running more than one copy".
`TRUSTED_PROXY_HOPS=0` means there is no trusted proxy in front of the app, and then both
forwarding headers are ignored **entirely**; the socket address wins. A chain shorter than
the count, or an entry that is not an IP address, cannot have come from our proxies either
and falls back to the socket address, never to the caller's value. `RemoteAddr` keeps its
documented `host:port` shape. Everything that needs a client address — the rate limiter,
`uploader_ip` — reads `RemoteAddr`; **no handler reads a forwarding header directly.**

Three smaller changes belong to the same decision, because a copy is only interchangeable
if all of them hold: migrations run under a Postgres **advisory lock** (a lock held in the
database itself), so several copies starting at once on an empty database do not race;
the pool size of one copy is `DB_POOL_MAX`; and health is reported by two separate
probes, `/livez` (is the process alive?) and `/readyz` (can this copy take traffic? —
the dependencies decide that, and only they), so a database blip drains traffic instead
of restarting the whole fleet.

## Alternatives

- **Presigned URLs** (short-lived links carrying a signature) — the reflex choice for
  object storage, rejected here: they expire, so a browser cannot cache the image and an
  SSR-rendered page goes stale; and the database would have to store the object *key* and
  every read path would have to mint a link, instead of storing a finished URL the way it
  already does.
- **Serving files through the application, proxying from the store** — keeps the old URL
  shape, and rejected: it makes every copy a file server again, puts every byte of every
  image through the app, and re-adds the exact route this change deleted.
- **A shared network volume mounted into every copy** — the smallest change, rejected
  because it needs a filesystem every copy can mount, which not every hosting product
  offers; the orchestrator is deliberately undecided.
- **A list of trusted proxy subnets** instead of a hop count — the *accurate* way to do
  this, and the only one that can tell "arrived through our proxy" from "arrived directly".
  Rejected in favour of the counter (the user's call): one number to configure, and no
  network-topology list to keep in sync. The price is spelled out below.
- **Keeping `X-Real-IP` trusted whatever the hop count** — rejected: it is forged just
  as easily as the other header. It is now gated on `TRUSTED_PROXY_HOPS >= 1`, so
  `TRUSTED_PROXY_HOPS=0` ignores it like everything else. Dropping the header *entirely*
  was the stricter option and was **not** taken: nginx deployments overwhelmingly send
  exactly this header, so refusing it would break the commonest setup there is. The
  residual risk of keeping it is spelled out below.
- **A feature flag to fall back to local-disk uploads** — rejected: two storage paths in
  the code forever, both needing to keep working. The rollback is `git revert`.

## Consequences

- A file written by any copy is readable by every other copy and by the browser, without
  the application being in the path at all.
- **The bucket is public for reads, and that is the security model.** The only protection
  an object has is that its key is a random UUID under a date prefix — which is what the
  old local directory offered too, since it was served publicly. Nothing private may go in
  this bucket.
- The path-traversal guard now lives in the key builder. It must stay covered by a test:
  it is the kind of protection that looks like dead string-handling to a future reader.
- The `S3_*` credentials are `required,notEmpty`, so a half-filled `.env` copied from the
  example stops the boot instead of failing on the first upload, in production, at the
  worst moment.
- **`TRUSTED_PROXY_HOPS` has to match the real chain, and both mistakes are silent.** Set
  it higher than the number of proxies actually in front of the app and the count reaches
  past them into the part of the header the caller wrote, so a caller that adds one entry
  of its own picks its bucket again. Set it lower and the address read belongs to one of
  our own proxies, so every client behind it collapses into a single shared bucket. The
  counter cannot detect either case, because it cannot see the topology — that is the cost
  knowingly accepted in exchange for one number instead of a subnet list.
- **The whole scheme rests on the proxy appending `X-Forwarded-For`, and the app cannot
  tell whether it does.** Put it behind a proxy that passes the header through instead —
  nginx with a bare `proxy_pass`, which sets no forwarding header at all — and the
  rightmost entry is the caller's own, so the counter hands out a bucket per forged
  address and stores a forged `uploader_ip`. It looks identical to a correct deployment
  from the inside: no error, no log line, nothing to assert in a test. Hence the explicit
  requirement, the config lines and the verification recipe in the meta-repo's
  `docs/DEPLOY.md`; hence also the stand's own `scale/Caddyfile` having to opt into
  `trusted_proxies` before it can even simulate a forgery.
- **Residual risk, knowingly kept: `X-Real-IP` is believed without counting anything.**
  When a request arrives with no `X-Forwarded-For` at all, any `TRUSTED_PROXY_HOPS >= 1`
  makes the app take `X-Real-IP` as the client address. The header carries no chain, so
  there is nothing to count and no way to tell a proxy's value from a caller's, and no
  proxy strips it by default — Caddy forwards a client-sent one untouched, and so does
  nginx with a bare `proxy_pass`. On a deployment whose proxy sets neither header, one
  `X-Real-IP` line picks the caller's own rate-limit key. Kept because nginx setups
  overwhelmingly send exactly this header; closed either by having the proxy overwrite it
  (`proxy_set_header X-Real-IP $remote_addr;`) or by `TRUSTED_PROXY_HOPS=0`.
- **Orphaned objects are possible and nothing collects them.** Writing the object and
  inserting the row that points at it are two steps and cannot be one transaction. A
  failed insert triggers a best-effort delete of the object, but a process killed or a
  network lost in the gap leaves an object no row references — and so does a `PutObject`
  that times out *after* the bytes landed, which is left alone on purpose: chasing it
  would add a second call, and a second timeout, against the same unresponsive storage on
  every failing upload. The invariant kept on every failure path is the other direction:
  **no row ever points at an object that is not there.** There is no sweeper and no
  bucket lifecycle rule; the cost is storage only, since an unreferenced key is a UUID
  nobody holds. Recorded in `docs/DEPLOY.md` under "Where uploaded files live".
- A rate limit now actually limits: a client that used to get a fresh bucket per request
  gets one bucket. If a legitimate caller starts seeing 429s after this change, the hop
  count is the first thing to check, not the limit.
