# ADR-0004: Route queries from the catalog

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-02, FR-04, INV-1, INV-2, [HLD §3.3](../design/hld.md#33-search)

## Context

A search can cover HOT, WARM, and COLD data. Partitions move between tiers
while searches are running. The router needs one consistent view of where
each partition can be read.

## Decision

The router plans every query from `obs_meta.data_partitions`. A migration
changes a partition's tier and location in a single PostgreSQL transaction,
guarded by a `version` column. The old location is kept for `purge_grace`,
which is longer than the maximum query timeout.

## Consequences

### Positive

- A tier switch takes effect atomically from the router's point of view.
- In-flight queries on the old location complete correctly.
- The catalog, together with `migration_jobs`, provides the data for RPT-04
  and RPT-06.

### Negative

- PostgreSQL is on the query path. The router caches the catalog for 5 s and
  returns `PARTIAL` if PostgreSQL is unavailable.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| OpenSearch aliases | An alias cannot point to Parquet files |
| Searching every tier for every query | Wastes work, and returns duplicates while a migration is in progress |
