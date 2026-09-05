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
| `graphql_error` | every GraphQL error | `error` (**never masked**), `code`, `path`, `request_id` |
| `panic_recovered` | HTTP handler panic | `panic`, `stack`, `method`, `path`, `request_id` |
| `graphql_panic` | panic **inside a resolver** (gqlgen recovers these, not the HTTP middleware) | `panic`, `stack`, `request_id` |
| `upload_error` | rejected or failed upload | `status`, `reason` (the client-facing message), `error` (the real cause), `request_id` |
| `db_query_slow` | query over 200 ms | `sql`, `duration_ms`, `request_id` |
| `db_query_failed` | query returned an error | `sql`, `duration_ms`, `error`, `request_id` |
| `job_started` / `job_finished` | each background job | `type`, `task_id`, `request_id`, `duration_ms`, `ok` |
| `job_panic` | panic inside a job | `type`, `task_id`, `panic`, `stack` |
| `job_failed` | job returned an error | `type`, `task_id`, `request_id`, `attempt`, `max_retry`, `error` |

**The level tells you whose fault it is.** `ERROR` = ours (5xx, internal GraphQL
error, failed job, failed query); `WARN` = the caller's (4xx, `UNAUTHENTICATED`,
`FORBIDDEN`, `BAD_USER_INPUT`, a slow query, a rejected token); `INFO` = routine.
So `level=ERROR` is a real incident feed, not a category of message.

**Correlation is the point:** every request-scoped line carries `request_id`
(and `user_id` once authenticated), added by `middleware.ContextLogger` and
`auth` and read via `logger.From(ctx)`. So domain logs (`profile created`,
`files uploaded`, …) tie back to the request that caused them — and so do
background jobs, whose `request_id` travels in the task payload and is put back
on the log by `queue.Worker.jobLogger`.

Two lines are deliberately **not** logged: query arguments (pgx hands them over
untyped, where redaction by key cannot help) and mutation variables (routinely
PII). Both show as `[REDACTED]`.

## The core workflow: from a failed request to its logs

1. A GraphQL error response includes `extensions.requestId` (see
   `internal/graph/errors.go`). Grab it.
2. Filter the logs by that id — you get the whole story of the request: the
   `http_request` access line, the `graphql_operation` line, and any domain
   lines in between.

With more than one copy of the app running, `docker compose logs app` already
interleaves all of them, and `request_id` is what separates the stories again — one
request is served start to finish by one copy, so filtering by the id gives you that
copy's account of it and nothing else. Add `--no-log-prefix` to drop the container
name, or leave it on to see *which* copy answered.

```sh
# dev (app on host): logs go to the terminal running `task start:dev`
task start:dev 2>&1 | grep '"request_id":"<ID>"'

# app in Docker:
docker compose logs -f app | grep '"request_id":"<ID>"'

# pretty-print + filter with jq:
docker compose logs --no-log-prefix app | jq -c 'select(.request_id=="<ID>")'

# only what is our fault (5xx, internal errors, failed jobs/queries):
docker compose logs --no-log-prefix app | jq -c 'select(.level=="ERROR")'

# everything that went wrong, ours and the caller's:
docker compose logs --no-log-prefix app | jq -c 'select(.level=="ERROR" or .level=="WARN")'

# slowest requests first:
docker compose logs --no-log-prefix app | jq -c 'select(.msg=="http_request")' |
  jq -s 'sort_by(-.duration_ms) | .[0:20][]'
```

## Symptom → cause → fix

| Symptom | Likely cause | Fix |
|---|---|---|
| GraphQL `code: UNAUTHENTICATED` (401) | No/invalid token; mock off | In dev set `OIDC_MOCK_ENABLED=true` (non-prod) and send `x-mock-sub`, or pass a valid `Authorization: Bearer …`. Check `OIDC_ISSUER/AUDIENCE/JWKS_URI`. |
| GraphQL `code: FORBIDDEN` (403) | Authenticated but lacks role | Roles live on the DB profile, **not** the token (`auth.RequireRole`). Grant the role in the DB (pgweb `:5100`). |
| GraphQL `code: INTERNAL_SERVER_ERROR` (500) | Unhandled domain error / panic | Find the `request_id`; the `graphql_error` line has the **unmasked** message even in production, and `graphql_panic` (resolver) or `panic_recovered` (HTTP handler) has the stack. |
| A request "just hangs" or the app feels slow | A query without an index, or one waiting on a lock | `jq -c 'select(.msg=="db_query_slow")'` — the `sql` and `duration_ms` are on the line, with the `request_id` that caused it. |
| Browser CORS error | Origin not allowed | Add the SPA origin to `CORS_ORIGIN` (comma-separated) and restart. |
| Frontend codegen fails | Backend not running / schema stale | Start backend (`task start:dev`); after schema edits run `task gen` and commit. |
| Migrations fail / schema drift | Local DB in a bad state | `task db:reset` (destroys local DB), then `task db:migrate`. |
| `/readyz` returns 503 | DB/Redis down or heap over threshold | Body names the failing check (db/redis/memory). Ensure `docker compose up -d db redis`. `/livez` stays 200 through all of this on purpose — it only says the process is alive. |
| Upload answers 500; `upload_error` has `reason: "Failed to save metadata"` | Read the `error` field on that line — it names the real cause, either the object storage or Postgres | Storage not running locally: `docker compose up -d garage garage-init` (`task start:dev` does not start it). Otherwise check `S3_ENDPOINT` — that is the address **the app** connects to, not the public `S3_PUBLIC_BASE_URL`. Nothing is half-written: a failure removes the objects already stored. |
| Upload succeeds but the returned link 404s in the browser | `S3_PUBLIC_BASE_URL` wrong | It must be the prefix **the browser** resolves, bucket name included; the URL is that value + `/` + the object key. |
| Background job “did nothing” | Job failed/panicked silently | Look for `job_failed` / `job_panic` by `type`/`task_id`; inspect queue state in Asynqmon (`:5300`). To find the request that enqueued it, filter by the job line's `request_id`. |

## Quick reference

- Liveness: `curl localhost:4000/livez` → `{"status":"ok"}` (process is up; touches
  nothing else — this is what the container HEALTHCHECK probes).
- Readiness: `curl localhost:4000/readyz` → per-dependency status, 503 if one is
  unusable — this is what a reverse proxy should gate traffic on.
- GraphQL IDE: `/playground` (Basic-Auth, dev login `admin`/`admin`).
- Dashboards (Basic-Auth): pgweb `:5100`, RedisInsight `:5200`, Asynqmon `:5300`.
- Schema (source of truth): `internal/graph/schema.graphqls` (every field documented).

## Going to production: deeper observability (not built in)

This template keeps observability to structured logs + health checks on purpose.
When a derived project needs more, add (in order of usual value):

- **Error tracking** — Sentry (or similar): ship `panic_recovered`/`graphql_panic`/`job_panic`
  with stacks to a remote service. DSN-gate it so it is a no-op when unset.
- **Metrics** — Prometheus `/metrics` + Grafana: request rate/latency, error
  rate by status, queue depth/retries, pool stats.
- **Tracing** — OpenTelemetry across HTTP → resolver → DB/queue (the otel deps
  are already present indirectly); propagate trace ids alongside `request_id`.
- **Profiling** — `net/http/pprof` behind admin auth for CPU/heap/goroutine
  investigations.
