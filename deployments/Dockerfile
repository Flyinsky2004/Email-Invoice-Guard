# syntax=docker/dockerfile:1
#
# Multi-stage build producing a static, non-root runtime image.
# CGO is disabled because the SQLite driver (modernc.org/sqlite) is pure Go.
#
# Base images are pinned to supported releases:
#   - golang:1.27.3-alpine3.24  (Go 1.27.3 on Alpine 3.24)
#   - alpine:3.24.4             (supported Alpine 3.24 line; 3.21 is EOL)
#   - caddy:2.11.9-alpine       (see deployments/docker-compose.yml)
# Sources: https://www.alpinelinux.org/releases/ and
#          https://github.com/caddyserver/caddy/releases/latest
#
# Build (from the repository root):
#   docker build -f deployments/Dockerfile -t email-invoice-guard:latest .

FROM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.24.4 AS runtime
# ca-certificates: outbound TLS to Graph/IMAP/Telegram.
# tzdata: correct Asia/Taipei calendar dates and reminder scheduling.
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 guard \
 && adduser -S -u 10001 -G guard -h /app -s /sbin/nologin guard \
 && mkdir -p /data \
 && chown 10001:10001 /data

WORKDIR /app
COPY --from=build /out/server /app/server

USER 10001:10001
ENV LISTEN_ADDRESS=:8080 \
    DATABASE_PATH=/data/invoice-guard.db \
    HEALTH_PATH=/healthz

VOLUME ["/data"]
EXPOSE 8080
STOPSIGNAL SIGTERM

# Uses the binary's own probe so the image needs no curl/wget.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/app/server", "--healthcheck"]

ENTRYPOINT ["/app/server"]
