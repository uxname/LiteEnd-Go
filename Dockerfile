# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
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
# Pre-create the uploads mountpoint so a fresh named volume inherits non-root
# ownership (distroless nonroot uid = 65532). Without this, the volume would be
# root-owned and the non-root app could not write uploads.
RUN mkdir -p /data/uploads

# ---- runtime stage (distroless static, non-root) ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=build --chown=65532:65532 /data/uploads /app/data/uploads
EXPOSE 4000
# Migrations run programmatically at startup (embedded), so no goose CLI needed.
HEALTHCHECK --interval=5s --timeout=10s --retries=3 --start-period=10s \
    CMD ["/app/server", "-healthcheck"]
ENTRYPOINT ["/app/server"]
