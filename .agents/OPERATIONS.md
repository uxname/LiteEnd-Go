# Operations — admin surfaces, data, environment

## Auth is mandatory on every admin surface

- External dashboards (pgweb, RedisInsight, Asynqmon) are exposed **only** through
  the Caddy Basic-Auth proxy (`admin_proxy` in `docker-compose.yml`, `Caddyfile`).
  **Never publish their container ports directly.**
- The app's own dev pages (`/dev`, `/playground`, `/swagger`, `/openapi.yaml`) are
  wrapped with `middleware.BasicAuth` using `ADMIN_USER` / `ADMIN_PASSWORD`.
- If you add a dashboard, put it behind the proxy too.

Credentials: `ADMIN_USER` / `ADMIN_PASSWORD` (Go side) and `ADMIN_PASSWORD_HASH`
(bcrypt, for Caddy — escape `$` as `$$` in `.env`). Keep all three in sync.

## Persistent state

All three stores are **named Docker volumes** — nothing is bind-mounted to the
repo. `docker-compose.yml` declares all three at the bottom;
`docker-compose.prod.yml` declares its own `uploads`:

| Volume | Holds | Owner uid |
|---|---|---|
| `postgres` | Postgres data (inspect via pgweb, not raw files) | postgres |
| `redis` | Redis data (inspect via RedisInsight) | redis |
| `uploads` | user uploads, served at `/uploads` | 65532 (app) |

Do **not** bind-mount Postgres/Redis data into the repo: root-owned `0700` files
under `./data` break `go test ./...`.

## Deploy

- `docker-compose.yml` — local dev, all-in-one (app + db + redis + dashboards).
- `docker-compose.prod.yml` — production, app-only; the production `.env` points
  `DATABASE_HOST`/`REDIS_HOST` at external Postgres/Redis.
- Images are named `liteend`, prefixed/tagged via `IMAGE_REGISTRY` / `IMAGE_TAG`;
  build and push with `task docker:build` / `task docker:push`.
- Full guide: the meta repo's `docs/DEPLOY.md`.

## Environment

`.env.example` is the documented contract — keep it in step with
`internal/config/config.go`, and see the meta-repo's `docs/ENV-CONTRACT.md` for the
pairs that must match the frontend.

Two variables deserve special care:

- **`NODE_ENV`** gates *all three* production hardenings in `config.Load`: it refuses
  `OIDC_MOCK_ENABLED`, requires a non-empty `CORS_ORIGIN`, and disables GraphQL
  introspection and internal error messages. The check is the exact string
  `production` (`IsProduction()`), and the default is `development` — so forgetting it
  ships a permissive deployment that looks fine.
- **`CORS_ORIGIN`** is a comma-separated allowlist that gates **both** HTTP CORS and
  the **WebSocket handshake** (`internal/graph/handler.go`). A value that is merely
  wrong for CORS now also refuses subscriptions from that origin. Entries are
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
