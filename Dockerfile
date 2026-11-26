# syntax=docker/dockerfile:1

# One image for every service (ADR-0009). Each container picks its binary
# with `command`. cgo is enabled because the COLD tier uses DuckDB.
FROM golang:1.27-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# DuckDB reads the object store through its httpfs extension. Fetch it at
# build time so the services do not need internet access when they start.
# The version must match the DuckDB library bundled with duckdb-go (go.mod).
ARG DUCKDB_VERSION=v2.0.1
ARG TARGETARCH
RUN mkdir -p /ext/${DUCKDB_VERSION}/linux_${TARGETARCH} \
    && curl -fsSL https://extensions.duckdb.org/${DUCKDB_VERSION}/linux_${TARGETARCH}/httpfs.duckdb_extension.gz \
    | gunzip > /ext/${DUCKDB_VERSION}/linux_${TARGETARCH}/httpfs.duckdb_extension

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --create-home app \
    && mkdir -p /data/inbox /spool && chown app /data/inbox /spool
COPY --from=build /out/ /usr/local/bin/
COPY --from=build --chown=app /ext/ /home/app/.duckdb/extensions/
USER app
