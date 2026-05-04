# syntax=docker/dockerfile:1.7
#
# rex-central runtime image.
#
# Two-stage: a Go builder produces a static-cgo-free binary, then
# distroless ships it. The binary is the same Go module the local
# rex CLI compiles from; per overview.SYS.1 the central is "the
# same Go module as the local-node binary; a build flag selects
# which surfaces are compiled in" — today that flag is just the
# choice of cmd/rex-central as the build target.

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
    -o /out/rex-central ./cmd/rex-central

# Runtime stage. distroless/static is ~2MB; combined with the Go
# binary this lands around 30MB compressed.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /

# /etc/rex is the canonical location central-node.DEPLOY.2
# specifies for the config file; mount the operator's
# central.toml here in the compose file.
COPY --from=builder /out/rex-central /usr/local/bin/rex-central

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/rex-central"]
CMD ["serve"]
