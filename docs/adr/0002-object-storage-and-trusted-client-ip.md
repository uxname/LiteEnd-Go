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
by the caller and only *appended to* by the proxies in front of us, so its leftmost entry
is a value the caller chose. `internal/middleware/ratelimit.go` keys the rate limit on that
same `RemoteAddr`, and `internal/upload/handler.go` wrote it into the database as
`uploader_ip`.

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
`X-Forwarded-For` — the entry our own proxy appended, which no caller can write.
`TRUSTED_PROXY_HOPS=0` means there is no trusted proxy in front of the app, and then both
forwarding headers are ignored **entirely**; the socket address wins. A chain shorter than
the count, or an entry that is not an IP address, cannot have come from our proxies either
and falls back to the socket address, never to the caller's value. `RemoteAddr` keeps its
documented `host:port` shape. Everything that needs a client address — the rate limiter,
`uploader_ip` — reads `RemoteAddr`; **no handler reads a forwarding header directly.**

Three smaller changes belong to the same decision, because a copy is only interchangeable
if all of them hold: migrations run under a Postgres **advisory lock** (a lock held in the
database itself), so several copies starting at once on an empty database do not race;
the pool size of one copy is `DB_POOL_MAX`; and the single `/health` probe is split into
`/livez` (is the process alive?) and `/readyz` (can this copy take traffic?), so a
database blip drains traffic instead of restarting the whole fleet. `/health` survives as
an alias of `/readyz` for deployments that already poll it.

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
- **Keeping `X-Real-IP` trusted when `X-Forwarded-For` is absent** — rejected: it is
  forged just as easily and carries no chain to count, so it can only be believed on a
  trusted proxy's word, exactly like the other header.
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
- A rate limit now actually limits: a client that used to get a fresh bucket per request
  gets one bucket. If a legitimate caller starts seeing 429s after this change, the hop
  count is the first thing to check, not the limit.
