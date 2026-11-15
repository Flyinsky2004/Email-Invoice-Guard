# Caddy's tagged upstream image was built with Go 1.27.3. Rebuild the pinned
# upstream source with the patched SDK (GO-2026-6859 / CVE-2026-97054).
FROM golang:1.27.4-alpine3.24 AS build
ENV CGO_ENABLED=0
RUN go install github.com/caddyserver/caddy/v2/cmd/caddy@v2.11.9

FROM alpine:3.24.4
RUN apk add --no-cache ca-certificates tzdata \
 && mkdir -p /etc/caddy /data /config
COPY --from=build /go/bin/caddy /usr/bin/caddy
COPY deployments/Caddyfile /etc/caddy/Caddyfile
ENV XDG_CONFIG_HOME=/config XDG_DATA_HOME=/data
VOLUME ["/data", "/config"]
EXPOSE 80 443 443/udp
STOPSIGNAL SIGTERM
ENTRYPOINT ["caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
