# ADR-0003: Store the COLD tier as Parquet, written and read through DuckDB

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-02, FR-03, SQ-4, [LLD §4.4](../design/lld.md#44-cold-object-layout), [LLD §6.4](../design/lld.md#64-router)

## Context

The brief asks for "cold" logs in cheap S3 or GCS storage that remain
searchable, and for a measured comparison of hot and cold search (SQ-4). The
cold format needs:

- Column statistics, so that queries can skip irrelevant data.
- Bloom filters, for fast exact-value lookups.
- A query engine that can read it directly from object storage.

## Decision

Store COLD partitions as ZSTD-compressed Parquet files sorted by
`event_time`. DuckDB writes bloom filters automatically for
dictionary-encoded columns. Use DuckDB, embedded through the `duckdb-go`
driver, both to write the files (in `dlm`) and to query them (in `router`).

## Consequences

### Positive

- One engine writes and reads the files, so statistics and bloom filters are
  consistent.
- No query cluster needs to run for COLD data.
- Gives a clear hot-versus-cold contrast to benchmark.

### Negative

- `duckdb-go` uses cgo. Images need a glibc base such as
  `debian:bookworm-slim` instead of a distroless static image (ADR-0009).
- A DuckDB connection is not safe for concurrent use, so the router needs a
  connection pool.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| A pure-Go Parquet writer with a DuckDB reader | Two engines to tune; statistics and bloom filters are harder to keep consistent |
| OpenSearch searchable snapshots | Needs nodes with the warm role, data is only queryable through OpenSearch, and it hides the hot-versus-cold contrast the brief asks to measure |
| Gzip-compressed JSON lines | No column statistics, so every query scans the whole file |
