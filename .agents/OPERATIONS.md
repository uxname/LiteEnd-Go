# Operations — admin surfaces, data, environment

## Auth is mandatory on every admin surface

- External dashboards (pgweb, RedisInsight, Asynqmon) are exposed **only** through
  the Caddy Basic-Auth proxy (`admin_proxy` in `docker-compose.yml`, `Caddyfile`).
  **Never publish their container ports directly.**
- The app's own dev pages (`/dev`, `/playground`, `/swagger`, `/openapi.yaml`) are
  wrapped with `middleware.BasicAuth` using `ADMIN_USER` / `ADMIN_PASSWORD`.
- If you add a dashboard, put it behind the proxy too.

Credentials: `ADMIN_USER` / `ADMIN_PASSWORD` (Go side) and `ADMIN_PASSWORD_HASH`
(bcrypt, for Caddy — escape `$` as `$$` for docker compose). Keep all three in sync.
Unset, all three fall back to `admin`/`admin`; the hash's dev fallback lives in
`docker-compose.yml`, not the `Caddyfile`, because Caddy's `{$VAR:default}` only fires for
an **unset** variable and compose always sets every key it lists — an empty one made Caddy
refuse to start.

## Persistent state

**The app container holds none.** Every byte it keeps lives in a service beside
it — Postgres, Redis, or the object store — so one copy of the app is
interchangeable with the next, and `docker-compose.prod.yml` declares no volume
at all.

Locally those services are **named Docker volumes** — nothing is bind-mounted to
the repo. `docker-compose.yml` declares all four at the bottom:

| Volume | Holds | Owner uid |
|---|---|---|
| `postgres` | Postgres data (inspect via pgweb, not raw files) | postgres |
| `redis` | Redis data (inspect via RedisInsight) | redis |
| `garage-meta` | Garage cluster metadata and the object index | root (the image sets no user) |
| `garage-data` | the uploaded files themselves | root (the image sets no user) |

Do **not** bind-mount Postgres/Redis data into the repo: root-owned `0700` files
under `./data` break `go test ./...`.

Uploaded files go to S3-compatible object storage (minio-go client), and the link
the API returns points **straight at the storage** — the app never serves file
bytes, and there is no download route to break. What that link IS depends on
`FILE_VISIBILITY`: private (the default) means a **signed** link that expires
after `FILE_LINK_TTL_MINUTES`, against a bucket closed to anonymous readers;
public means the permanent link ADR-0002 used to hand out, against an open
bucket. Dev and the scale stand run a [Garage](https://garagehq.deuxfleurs.fr)
for this, initialized on `up` by a `garage-init` container (layout, access key,
bucket, and opening or closing it to match the mode — no manual step).
That initializer needs **Docker Engine 27.4+**: it mounts the Garage binary out of
the Garage image (`type: image`), the only way to run a CLI from an image built
`FROM scratch`.

## Health probes

Two endpoints, not interchangeable:

- **`/livez`** — "is this process alive?", touches nothing external. The image's
  `HEALTHCHECK` (`server -healthcheck`) probes this, because an orchestrator
  *restarts* whatever fails liveness: if it pinged the database, one database blip
  would restart every copy at once.
- **`/readyz`** — "can this copy serve traffic?": Postgres and Redis, 503 when
  one is unusable. The heap reading rides along in the body as diagnostics and
  never changes the answer — a load spike would otherwise pull every copy out of
  rotation at once, and restarting a leaking copy is liveness' job. This is what the **reverse proxy** should gate traffic on,
  so a copy with a sick dependency is skipped rather than killed.

## Deploy

- `docker-compose.yml` — local dev, all-in-one (app + db + redis + Garage +
  dashboards).
- `docker-compose.prod.yml` — production, **app only**: Postgres, Redis and the
  object storage are external services the production config points
  `DATABASE_*`, `REDIS_*` and `S3_*` at.
- It publishes **no host port**. It joins an existing external network named by
  `PROXY_NETWORK` (default `dokploy-network`) where the reverse proxy already
  runs, and the proxy reaches port 4000 directly. That is what lets several
  copies run side by side: `docker compose -f docker-compose.prod.yml up -d
  --scale app=N`.
- Migrations run on startup under a Postgres advisory lock, so copies starting at
  the same moment against an empty database queue up instead of racing.
- Before raising N, size two variables: `DB_POOL_MAX` and `TRUSTED_PROXY_HOPS`
  (below).
- Images are named `liteend`, prefixed/tagged via `IMAGE_REGISTRY` / `IMAGE_TAG`;
  build and push with `task docker:build` / `task docker:push`.
- Full guide: the meta repo's `docs/DEPLOY.md`. Why storage and the client
  address are shaped this way:
  [ADR-0002](../docs/adr/0002-object-storage-and-trusted-client-ip.md).

## Environment

`.env.example` is the documented contract — keep it in step with
`internal/config/config.go`, and see the meta-repo's `docs/ENV-CONTRACT.md` for the
pairs that must match the frontend.

Four variables deserve special care:

- **`NODE_ENV`** gates *all three* production hardenings in `config.Load`: it refuses
  `OIDC_MOCK_ENABLED`, requires a non-empty `CORS_ORIGIN`, and disables GraphQL
  introspection and internal error messages. The check is the exact string
  `production` (`IsProduction()`), and the default is `development` — so forgetting it
  ships a permissive deployment that looks fine.
- **`CORS_ORIGIN`** is a comma-separated allowlist that gates **both** HTTP CORS and
  the **WebSocket handshake** (`internal/graph/handler.go`). A value that is merely
  wrong for CORS also refuses subscriptions from that origin. Entries are
  trimmed. An empty list splits the two consumers: HTTP CORS falls back to allow-any
  (go-chi/cors default), while the WebSocket handshake is **never** allow-all — it
  then admits only same-origin and Origin-less (non-browser) clients, in every
  environment. Production refuses to boot on an empty list; set it explicitly
  everywhere else too, including staging.

  **Never write `CORS_ORIGIN=*`.** The two consumers disagree about it: go-chi/cors
  treats `*` as allow-all, while the WebSocket patterns are matched against
  `scheme://host` with `path.Match`, and `*` does not match the `/` in it. The result
  is every HTTP origin allowed and every browser WebSocket handshake refused with 403.
  List origins explicitly instead.

- **`DB_POOL_MAX`** (default 10) is the pool size of **one** copy. The sizing rule,
  spelled the same way in `.env.example`, `internal/config/config.go` and
  `internal/db/pool.go`: replicas x `DB_POOL_MAX` must stay below the Postgres
  `max_connections` limit (default 100). Leave room under it for migrations, `psql`
  sessions and the dashboards. Cross it and Postgres refuses new connections —
  copies start failing `/readyz` in turn while each one looks fine on its own.

- **`TRUSTED_PROXY_HOPS`** (default 1) is how many reverse proxies actually sit in
  front of this app. The client address — the rate limiter's key — is taken that
  many entries from the **right** of `X-Forwarded-For`, because a proxy that appends
  to that header leaves only the rightmost entries beyond the caller's reach.
  **That is a requirement on your proxy, not a property of the header**: a proxy
  configured to forward the request untouched appends nothing, and then the
  rightmost entry is the caller's own. `docs/DEPLOY.md` in the meta-repo carries the
  per-proxy settings and a recipe for checking yours.
  `0` means no proxy, and then `X-Forwarded-For` and `X-Real-IP` are ignored
  entirely. Two is a perfectly normal value (a CDN or cloud load balancer in front
  of your own proxy). Both mistakes are silent: too **high** and the caller pads the
  header until the count reaches its own entries, picking its rate-limit key and
  getting a fresh bucket per forged value; too **low** and you read an address one
  of your proxies wrote about another, which is identical for everyone, so the whole
  internet shares one bucket and normal traffic starts hitting 429.

Storage (`S3_*`) has one trap worth stating: **`S3_ENDPOINT` and
`S3_PUBLIC_BASE_URL` are two different addresses of the same bucket** — the first
as the app reaches it from inside the network, the second as the browser resolves
it from outside, bucket name included. A file's permanent address is the second
value plus `/` plus the object key, and that string is what lands in the database.

Two more (`FILE_*`), and they are a security setting, not a tuning knob:

| Variable | What it does |
|---|---|
| `FILE_VISIBILITY` (default `private`) | `private`: the bucket refuses anonymous readers and every download needs a link the API signed. `public`: the bucket is world-readable and links never expire. The value also decides what `garage-init` does to the bucket, so changing it is a variable **and** an `up -d`. |
| `FILE_LINK_TTL_MINUTES` (default 60) | How long a signed link lives, 1…10080 (the S3 signature limit). A link is a bearer token for one object, so shorter is safer — but it must outlive the page holding it, or the user sees a broken image. An hour covers a cached page, a long lazily-loaded list and an idle tab. |

In private mode `S3_PUBLIC_BASE_URL` must be `<public S3 API address>/<bucket>`,
because a signed link is that prefix plus the key and a SigV4 signature covers
the host and path it was made for. The app refuses to boot otherwise, and any
proxy in front of the bucket must pass `/<bucket>/*` through **unchanged** — a
rewritten path or `Host` turns every link into `SignatureDoesNotMatch`. Why files
are private at all: [ADR-0003](../docs/adr/0003-files-are-private-and-served-through-signed-links.md).

## The proxy in front of the app

Everything below follows from one fact: `X-Forwarded-For` is an ordinary header the
caller writes freely, and the rightmost entries are trustworthy **only because a proxy
put them there**. The app cannot check that — a hop count sees addresses, never who
wrote which — so the requirement is on the proxy and it is not optional.

### nginx appends nothing unless you tell it to

A bare `proxy_pass` forwards the caller's `X-Forwarded-For` and `X-Real-IP` untouched,
which is the silent hole above. Two lines close it:

```nginx
location / {
    proxy_pass http://backend:4000;

    # $proxy_add_x_forwarded_for = what the caller sent, plus the address the
    # connection actually came from. That appended entry is the one the app counts
    # to, so "X-Forwarded-For: 9.9.9.9" arrives as "9.9.9.9, <real address>" and
    # TRUSTED_PROXY_HOPS=1 reads the real one.
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    # $remote_addr OVERWRITES X-Real-IP, so a forged one cannot survive the hop.
    proxy_set_header X-Real-IP       $remote_addr;

    proxy_set_header Host $host;
}
```

### Caddy replaces instead of appending

Out of the box Caddy **discards** a client-sent `X-Forwarded-For` and writes the peer
address: one entry, nothing forged, `TRUSTED_PROXY_HOPS=1` correct. Adding
`trusted_proxies` changes that — for requests from a listed range Caddy keeps what
arrived and appends, so every trusted hop is one more entry to count. `scale/Caddyfile`
in the meta-repo sets `trusted_proxies static private_ranges` deliberately, because the
stand has to push a forged header *through* the proxy to test the counting. That line
belongs to a loopback stand and nowhere else.

HAProxy writes no `X-Forwarded-For` at all until `option forwardfor` is set. Whatever
sits in front, check it rather than trust a paragraph.

### Check a live deployment in one request

`LOG_LEVEL=info` (the default) writes an `http_request` line per request, and its
`remote` field is the address the app settled on:

```bash
# Through your public entry point. 9.9.9.9 stands in for any address you do not own.
curl -s -o /dev/null -H 'X-Forwarded-For: 9.9.9.9' https://api.example.com/livez
docker compose -f docker-compose.prod.yml logs --tail=5 app | grep http_request
```

| `remote` in that line | Verdict |
|---|---|
| your own public address | Correct: the proxy appended and the hop count matches. |
| `9.9.9.9` | **Broken and exploitable.** The proxy is not appending. Fix the proxy — no hop count repairs this. |
| the proxy's own address | `TRUSTED_PROXY_HOPS` is too low, so every client shares one bucket. |

The limiter answers from the other end too — its Redis key *is* the address it counted:

```bash
# rate:rl:<ip>, or rate:rl:auth:<ip> for /graphql and /upload
redis-cli --scan --pattern 'rate:rl:*'
```

A `rate:rl:9.9.9.9` key means the forged value bought its own bucket.

### Give the readiness probe more than 5 seconds

`/readyz` pings Postgres and Redis under a 5-second budget of its own
(`config.HealthCheckTimeout`). A proxy whose probe timeout is shorter cuts the answer
off mid-flight, so live dependencies are reported unavailable: the copy leaves rotation
and the log gets a warning per dependency that never failed. Set the proxy's health
timeout above 5s — `scale/Caddyfile` uses 6s for exactly this reason.

### Known limit: `X-Real-IP` is believed without counting

With **no** `X-Forwarded-For` at all, the app falls back to `X-Real-IP` as long as
`TRUSTED_PROXY_HOPS >= 1`. That header carries no chain, so there is nothing to count
and nothing to verify: it is believed on the proxy's word alone. No proxy strips it for
you — Caddy passes a client-sent `X-Real-IP` through unchanged, and so does nginx with
a bare `proxy_pass`. On a deployment whose proxy sets neither header, one `X-Real-IP`
lets a caller pick its own rate-limit key.

The support is deliberate: nginx setups overwhelmingly send exactly this header, and
refusing it would break the most common deployment there is. Two ways to close it — the
`proxy_set_header X-Real-IP $remote_addr;` line above, or `TRUSTED_PROXY_HOPS=0`, which
ignores both headers at the cost of having no proxy in front of the app.
