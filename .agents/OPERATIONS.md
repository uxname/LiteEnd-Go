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

All four stores are **named Docker volumes**, declared at the bottom of
`docker-compose.yml` — nothing is bind-mounted to the repo:

| Volume | Holds | Owner uid |
|---|---|---|
| `postgres` | Postgres data (inspect via pgweb, not raw files) | postgres |
| `redis` | Redis data (inspect via RedisInsight) | redis |
| `uploads` | user uploads, served at `/uploads` | 65532 (app) |
| `backups` | `pg_dump` archives written by `db_backup` | 70 |

Do **not** bind-mount Postgres/Redis data into the repo: root-owned `0700` files
under `./data` break `go test ./...`.

## Backups — read before touching `internal/backup`

- `BACKUP_ROTATION` must be ≥ 1 and `BACKUP_FORMAT` must be `plain` or `custom`;
  `LoadBackup` refuses anything else at startup. Both were previously unvalidated and
  silently destructive (rotation `0` deleted the dump it had just created).
- The dump **extension decides the restore tool**: `.sql`/`.sql.gz` restore through
  `psql -v ON_ERROR_STOP=1`, `.dump` (the binary custom format) through
  `pg_restore --exit-on-error`. If you change naming or format, change `Restore` in
  the same commit — a mismatch produces backups that look green for months and are
  unusable on recovery day.
- Rotation prunes only the extension it currently produces, so switching
  `BACKUP_FORMAT` never deletes the dumps that are still the only restorable ones
  (it also means the old generation is never cleaned up — do that by hand).
- A restore must fail loudly. Never drop `ON_ERROR_STOP` / `--exit-on-error`:
  without them a half-restored database reports success.

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
  trimmed, and an empty list means "allow any origin" — acceptable in dev, refused in
  production, and *not* refused on other non-production environments, so set it
  explicitly on staging.

  **Never write `CORS_ORIGIN=*`.** The two consumers disagree about it: go-chi/cors
  treats `*` as allow-all, while the WebSocket patterns are matched against
  `scheme://host` with `path.Match`, and `*` does not match the `/` in it. The result
  is every HTTP origin allowed and every browser WebSocket handshake refused with 403.
  List origins explicitly instead.
