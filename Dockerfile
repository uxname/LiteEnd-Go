# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
# No git here: go.mod has no `replace`, the module path is public, and .git is not in
# the build context. A derived product with private Go modules (GOPRIVATE, or
# GOPROXY=direct) needs it back.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
      -X github.com/uxname/liteend-go/internal/version.Commit=${COMMIT} \
      -X github.com/uxname/liteend-go/internal/version.BuildTime=${BUILD_TIME}" \
    -o /out/server ./cmd/server

# ---- runtime stage (distroless static, non-root) ----
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 4000
# Migrations run programmatically at startup (embedded), so no goose CLI needed.
# The probe hits liveness (see cmd/server/main.go): a failed HEALTHCHECK makes an
# orchestrator restart the container, so it must not depend on the database.
# Traffic gating is the proxy's job, against /readyz.
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=10s \
    CMD ["/app/server", "-healthcheck"]
ENTRYPOINT ["/app/server"]
