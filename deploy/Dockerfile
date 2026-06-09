# syntax=docker/dockerfile:1.7
#
# rex-central runtime image.
#
# Two-stage: a Go builder produces a static-cgo-free binary, then
# distroless ships it. The binary is the same Go module the local
# rex CLI compiles from; the central node source is parked under
# rex-centralized/ while it is being broken out into its own service
# (git-ignored, but present in the build context). The build target
# is rex-centralized/cmd/rex-central.

FROM golang:1.25-alpine AS builder
WORKDIR /src

# Cache go.sum-pinned deps independently of source so source-only
# edits skip the download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Pure-Go pgx driver + modernc.org/sqlite for the local search
# index — both cgo-free, so CGO_ENABLED=0 keeps the image static.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build -trimpath -ldflags="-s -w" \
    -o /out/rex-central ./rex-centralized/cmd/rex-central

# Runtime stage.
#
# Switched from distroless/static to alpine because BACKUP.1
# requires literal pg_dump and BACKUP.3 requires literal
# pg_restore. Bundling postgresql-client keeps the binary
# self-contained: scheduled backups + `rex-central restore`
# work out of the box, no sidecar, no host dependency. Cost is
# the image growing from ~20MB (distroless) to ~50MB (alpine +
# postgresql17-client). Worth it for a one-flag deployment.
FROM alpine:3.21
WORKDIR /

RUN apk add --no-cache \
    ca-certificates \
    postgresql17-client \
 && addgroup -S rex \
 && adduser -S -G rex -h /var/lib/rex rex \
 && mkdir -p /var/lib/rex \
 && chown -R rex:rex /var/lib/rex

# /etc/rex is the canonical location central-node.DEPLOY.2
# specifies for the config file; mount the operator's
# central.toml here in the compose file.
COPY --from=builder /out/rex-central /usr/local/bin/rex-central

USER rex
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/rex-central"]
CMD ["serve"]
