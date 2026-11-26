# ADR-0009: Build all services from one Go module into one image

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: NFR-02, ADR-0003, [HLD §7](../design/hld.md#7-deployment)
- Amended by: [ADR-0014](0014-hourly-rollups-in-dlm.md), which removes the `aggregator` service

## Context

The system has five Go services that share the event schema, catalog access
code, configuration, and telemetry. ADR-0003 requires cgo for DuckDB.

## Decision

Use one Go module with `cmd/{ingest,dlm,router,aggregator,loadgen}` and shared
code under `internal/`. A multi-stage Dockerfile builds every binary into one
image based on `debian:bookworm-slim`. The image runs as a non-root user. Each
Compose service or Kubernetes Deployment selects its binary with `command`.

## Consequences

### Positive

- One build, one vulnerability scan, and one version tag for the whole system.
- Shared code cannot drift between services.

### Negative

- The image is larger than separate distroless images.
- A change to one service rebuilds the image for all of them.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| One image per service | Five times the build and scan work, with no benefit at this scale |
| One repository per service | Shared code would need versioning across repositories |
