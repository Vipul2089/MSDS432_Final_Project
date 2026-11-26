# ADR-0001: Partition data by time bucket

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: SQ-1, NFR-03, [LLD §4](../design/lld.md#4-partitioning-and-naming)

## Context

Phase 2 proposed one OpenSearch index per source per day. At the target
volume of 100 GB over 90 days, each daily index for one source holds about
0.28 GB. Keeping about 30 days in HOT and WARM would create about 120 primary
shards. OpenSearch recommends shards of 10–50 GB, because every shard uses
heap memory and adds work to every search.

The DLM also needs one unit that it can seal, copy, verify, and switch as a
whole.

## Decision

Partition data by time bucket, with one partition and one index per bucket
shared by all sources.

- The bucket size comes from the active lifecycle policy: one day by default,
  one hour in demo mode.
- Indices are named `logs-YYYY.MM.DD`, or `logs-YYYY.MM.DD.HH` for hourly
  buckets. `source` is a keyword field, not part of the index name.
- Primary shards = `max(1, ceil(avg_daily_gb / target_shard_gb))`.
- Events that arrive after their partition has been sealed are rejected with
  the reason `LATE_EVENT`.

## Consequences

### Positive

- About 4 times fewer shards at the target volume.
- Every partition covers a fixed time range, so the router can plan queries
  from the catalog alone.
- A sealed partition never changes, so COLD files never need to be reopened.

### Negative

- All sources share one lifecycle policy.
- Late events are lost to search. They are counted in `rejected_records` and
  shown in RPT-02.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| One index per source per day | Creates too many small shards at the target volume |
| Separate segments for late events | Correct, but adds complexity the MVP does not need |
| Rollover by size only | Time ranges vary, which complicates query planning and age-based policies |
