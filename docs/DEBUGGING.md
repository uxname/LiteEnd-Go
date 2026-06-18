# Debugging & diagnostics (backend)

A runbook for reading logs and triaging failures fast — written so a human **or
an AI agent** can go from a symptom to a cause without reverse-engineering the
codebase. Pair it with `internal/graph/errors.go` (error codes) and
[AGENTS.md](../AGENTS.md).

## How logs look

Logs are **structured JSON on stdout** (`log/slog`, see `internal/logger`).
Level via `LOG_LEVEL` (`debug|info|warn|error`, default `info`). Sensitive keys
(`password`, `token`, `secret`, `authorization`, `credentials`, `cookie`, `sig`)
are redacted to `[REDACTED]` automatically.

Key log lines (the `msg` field):

| `msg` | When | Notable fields |
|---|---|---|
| `http_request` | every HTTP request | `method`, `path`, `status`, `duration_ms`, `bytes`, `request_id`, `remote` |
| `graphql_operation` | every GraphQL op | `operation`, `type`, `variables` (redacted), `duration_ms`, `errors`, `request_id`, `user_id` |
| `panic_recovered` | HTTP handler panic | `panic`, `stack`, `request_id` |
| `job_started` / `job_finished` | each background job | `type`, `task_id`, `duration_ms`, `ok` |
| `job_panic` | panic inside a job | `type`, `panic`, `stack` |
| `job_failed` | job returned an error | `type`, `task_id`, `attempt`, `max_retry`, `error` |

**Correlation is the point:** every request-scoped line carries `request_id`
(and `user_id` once authenticated), added by `middleware.ContextLogger` and
`auth` and read via `logger.From(ctx)`. So domain logs (`profile created`,
`files uploaded`, …) tie back to the request that caused them.

## The core workflow: from a failed request to its logs

1. A GraphQL error response includes `extensions.requestId` (see
   `internal/graph/errors.go`). Grab it.
2. Filter the logs by that id — you get the whole story of the request: the
   `http_request` access line, the `graphql_operation` line, and any domain
   lines in between.

```sh
# dev (app on host): logs go to the terminal running `task start:dev`
task start:dev 2>&1 | grep '"request_id":"<ID>"'

# app in Docker:
docker compose logs -f backend | grep '"request_id":"<ID>"'

# pretty-print + filter with jq:
docker compose logs --no-log-prefix backend | jq -c 'select(.request_id=="<ID>")'

# only errors:
docker compose logs --no-log-prefix backend | jq -c 'select(.level=="ERROR")'
```

## Symptom → cause → fix

| Symptom | Likely cause | Fix |
|---|---|---|
| GraphQL `code: UNAUTHENTICATED` (401) | No/invalid token; mock off | In dev set `OIDC_MOCK_ENABLED=true` (non-prod) and send `x-mock-sub`, or pass a valid `Authorization: Bearer …`. Check `OIDC_ISSUER/AUDIENCE/JWKS_URI`. |
| GraphQL `code: FORBIDDEN` (403) | Authenticated but lacks role | Roles live on the DB profile, **not** the token (`auth.RequireRole`). Grant the role in the DB (pgweb `:5100`). |
| GraphQL `code: INTERNAL_SERVER_ERROR` (500) | Unhandled domain error / panic | Find the `request_id`; look for `panic_recovered` (has `stack`) or the wrapped `error` on the line. |
| Browser CORS error | Origin not allowed | Add the SPA origin to `CORS_ORIGIN` (comma-separated) and restart. |
| Frontend codegen fails | Backend not running / schema stale | Start backend (`task start:dev`); after schema edits run `task gen` and commit. |
| Migrations fail / schema drift | Local DB in a bad state | `task db:reset` (destroys local DB), then `task db:migrate`. |
| `/health` returns 503 | DB/Redis down or heap over threshold | Body names the failing check (db/redis/memory). Ensure `docker compose up -d db redis`. |
| Background job “did nothing” | Job failed/panicked silently | Look for `job_failed` / `job_panic` by `type`/`task_id`; inspect queue state in Asynqmon (`:5300`). |

## Quick reference

- Health: `curl localhost:4000/health` → `{"status":"ok"}`.
- GraphQL IDE: `/playground` (Basic-Auth, dev login `admin`/`admin`).
- Dashboards (Basic-Auth): pgweb `:5100`, RedisInsight `:5200`, Asynqmon `:5300`.
- Schema (source of truth): `internal/graph/schema.graphqls` (every field documented).

## Going to production: deeper observability (not built in)

This template keeps observability to structured logs + health checks on purpose.
When a derived project needs more, add (in order of usual value):

- **Error tracking** — Sentry (or similar): ship `panic_recovered`/`job_panic`
  with stacks to a remote service. DSN-gate it so it is a no-op when unset.
- **Metrics** — Prometheus `/metrics` + Grafana: request rate/latency, error
  rate by status, queue depth/retries, pool stats.
- **Tracing** — OpenTelemetry across HTTP → resolver → DB/queue (the otel deps
  are already present indirectly); propagate trace ids alongside `request_id`.
- **Profiling** — `net/http/pprof` behind admin auth for CPU/heap/goroutine
  investigations.
