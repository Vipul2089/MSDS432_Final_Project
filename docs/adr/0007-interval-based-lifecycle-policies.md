# ADR-0007: Express lifecycle policies as intervals

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-03, [LLD §7.2](../design/lld.md#72-lifecycle-policies)

## Context

Phase 2 stored `hot_days`, `warm_days`, and `retention_days` as integers. With
daily units, the class demonstration could not show any tier transition.

## Decision

Store `bucket_size`, `hot_after`, `warm_after`, `retain_for`, `late_window`,
and `purge_grace` as PostgreSQL `INTERVAL` values in named policies. Exactly
one policy is active. A `demo` policy uses hourly buckets and moves data
after hours instead of days. Because `loadgen` backfills several hours of
data, one demo shows partitions in all three tiers within minutes.

## Consequences

### Positive

- The same code runs both in production timescales and in a demo.
- Integration tests run in minutes.

### Negative

- Hourly buckets in demo mode create more, smaller partitions. This is
  acceptable for demos only.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| Integer days | No transitions can be shown during a class session |
| A fake clock that runs faster | Harder to reason about, and confusing in dashboards |
