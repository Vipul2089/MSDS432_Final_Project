# Architecture decision records

This directory holds the Architecture Decision Records (ADRs) for
Observability Fabric. Each ADR records one significant decision: its context,
the decision itself, its consequences, and the alternatives that were
rejected. Identifiers and abbreviations used in the records are defined in
the [glossary](../glossary.md).

## Process

1. Copy [`template.md`](template.md) to `NNNN-short-title-in-kebab-case.md`,
   using the next free number.
2. Set the status to `Proposed`, then open a pull request for team review.
3. When the team agrees, change the status to `Accepted`.
4. Never edit an accepted ADR to reverse it. Write a new ADR, and set the old
   one's status to `Superseded by ADR-NNNN`.

Statuses: `Proposed`, `Accepted`, `Rejected`, `Deprecated`,
`Superseded by ADR-NNNN`.

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-partition-by-time-bucket.md) | Partition data by time bucket | Accepted |
| [0002](0002-dlm-owns-all-tier-transitions.md) | The DLM performs every tier transition | Accepted |
| [0003](0003-parquet-via-duckdb-for-cold-tier.md) | Store the COLD tier as Parquet, written and read through DuckDB | Accepted |
| [0004](0004-catalog-based-query-routing.md) | Route queries from the catalog | Accepted |
| [0005](0005-resumable-single-worker-migration-jobs.md) | Run migrations as resumable jobs with a single worker | Accepted |
| [0006](0006-manifest-last-commit-for-cold-writes.md) | Commit COLD writes by writing the manifest last | Accepted |
| [0007](0007-interval-based-lifecycle-policies.md) | Express lifecycle policies as intervals | Accepted |
| [0008](0008-no-message-queue-in-mvp.md) | Use no message queue in the MVP | Accepted |
| [0009](0009-single-module-single-image.md) | Build all services from one Go module into one image | Accepted |
| [0010](0010-cold-text-search-by-regex-scan.md) | Search COLD text by regex scan | Superseded by ADR-0013 |
| [0011](0011-deterministic-event-id-from-origin-and-offset.md) | Derive `event_id` from origin and offset | Accepted |
| [0012](0012-watchdog-based-liveness-probe.md) | Base the liveness probe on an internal watchdog | Accepted |
| [0013](0013-shared-search-text-parser.md) | Parse search text once for all tiers | Accepted |
| [0014](0014-hourly-rollups-in-dlm.md) | Build hourly rollups in dlm, without an aggregator service | Accepted |
