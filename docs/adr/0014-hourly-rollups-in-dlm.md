# ADR-0014: Build hourly rollups in dlm, without an aggregator service

- Status: Accepted
- Date: 2026-11-26
- Deciders: Group 6
- Related: FR-05, FR-07, RPT-02 to RPT-04, [LLD §5.2](../design/lld.md#52-obs_gold),
  [LLD §6.5](../design/lld.md#65-hourly-rollup); amends the service list in
  [ADR-0009](0009-single-module-single-image.md)

## Context

The design had a separate `aggregator` service that, every 5 minutes, wrote
hourly event counts, anomaly flags and daily storage-cost rows to `obs_gold`.
The counts must be read from OpenSearch before a partition leaves it, because
COLD has no index to aggregate. `dlm` already visits every open partition each
plan cycle and is the only component that seals partitions and moves them.

## Decision

`dlm` writes the hourly rollup; there is no `aggregator` service.

- Each plan cycle, `dlm` runs one OpenSearch aggregation per open partition
  and replaces that partition's hours in `obs_gold.fact_events_hourly`.
- After a partition is sealed, `dlm` runs a final rollup and sets
  `gold_final_at`. A partition moves to COLD only after that.
- The anomaly flag (FR-07) is the view `obs_gold.events_hourly_scored`, which
  computes the score when it is queried.
- Storage cost is computed when the dashboard is read, from
  `data_partitions.size_bytes` and the `tier_prices` table. There is no
  `fact_tier_storage_daily` table.
- `obs_gold` is a schema in the same database as the catalog, so one
  connection and one migration path serve both.

## Consequences

### Positive

- One service fewer to build, deploy, monitor and test.
- The rule "roll up before the index is deleted" is enforced by the component
  that deletes indices, in the same planning step.
- Anomaly scores are never stale, because nothing stores them.

### Negative

- `dlm` now has a reporting duty next to lifecycle management.
- There is no daily history of storage per tier; the dashboard shows the
  current state.
- Partitions moved to COLD before this change have no hourly rows.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| A separate `aggregator` service | About 100 lines of work, but another container, configuration and failure mode, and it would still need to coordinate with `dlm` before COLD moves |
| Count events in `ingest` as they are loaded | Not repeatable: a crash after indexing but before counting, or a reloaded file, would leave the counts wrong |
| Grafana's OpenSearch data source | Covers HOT and WARM only, and the plugin is downloaded when Grafana starts |
