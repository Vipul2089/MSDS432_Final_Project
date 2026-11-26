# Observability Fabric: low-level design

| Field | Value |
| --- | --- |
| Document ID | LLD-001 |
| Version | 1.8 |
| Status | Baselined |
| Authors | Group 6: Gajendra Tiwari, Thiru Narayanasamy, Vipul Shukla |
| Approved by | Gajendra Tiwari |
| Date | 2026-11-09 |
| Related | [SRS-001](requirements.md), [HLD-001](hld.md), [ADRs](../adr/README.md) |

## Table of contents

- [1. Introduction](#1-introduction)
- [2. Code structure](#2-code-structure)
- [3. Event schema](#3-event-schema)
- [4. Partitioning and naming](#4-partitioning-and-naming)
- [5. Database schema](#5-database-schema)
- [6. Service design](#6-service-design)
- [7. Configuration](#7-configuration)
- [8. Health endpoints and probes](#8-health-endpoints-and-probes)
- [9. Error handling and logging](#9-error-handling-and-logging)
- [10. Test design](#10-test-design)
- [11. Validation spikes](#11-validation-spikes)
- [12. Revision history](#12-revision-history)

## 1. Introduction

This document specifies the internals of each component in
[HLD-001](hld.md): code layout, data formats, schemas, algorithms, interfaces,
configuration, and tests. It is detailed enough to implement milestones M0 to
M5 without further design work. Identifiers such as `FT-` (failure test),
`BQ-` (benchmark query) and `SP-` (validation spike), and all abbreviations,
are defined in the [glossary](../glossary.md).

## 2. Code structure

One Go module builds every service ([ADR-0009](../adr/0009-single-module-single-image.md)).

```text
.
├── cmd/
│   ├── dlm/              main packages: read settings, wire dependencies, start loops
│   ├── ingest/
│   ├── loadgen/          Synthetic log file generator (a command-line tool, not a service)
│   └── router/
├── internal/
│   ├── catalog/          PostgreSQL access: partitions, jobs, batches, query audit, hourly rollup
│   ├── cold/             COLD object layout, manifest, DuckDB set-up, fingerprint
│   ├── config/           Settings from environment variables, with validation
│   ├── dlm/              Planner, job runner, WARM to COLD export, hourly rollup
│   ├── event/            Event type, event ID and hash, reject reasons
│   ├── ingest/           Inbox watcher, parse and bulk-load pipeline
│   ├── objectstore/      S3 API client for SeaweedFS and GCS
│   ├── router/           Search API: request and text-query parsing, planning, HOT and COLD search, merge, audit
│   ├── search/           OpenSearch client: index template, bulk, search, PIT export, index admin
│   ├── server/           /healthz, /readyz and /metrics endpoints
│   └── source/
│       └── nslkdd/       NSL-KDD CSV mapper
├── migrations/           Numbered SQL migrations (golang-migrate: NNNN_name.up.sql and .down.sql)
├── deploy/
│   ├── compose/          compose.yaml and Prometheus configuration
│   └── grafana/          Provisioned data sources and dashboards (JSON)
├── scripts/              demo.sh, run by make demo
├── test/
│   └── failure/          Failure tests FT-01 and FT-02 (run.py)
├── Dockerfile
├── go.mod
└── Makefile
```

Not built yet: the CloudTrail mapper (`internal/source/cloudtrail`, optional),
Kubernetes manifests (`deploy/k8s`, stretch goal), benchmark queries BQ-1 to
BQ-3, and the watchdog behind `/healthz`
([ADR-0012](../adr/0012-watchdog-based-liveness-probe.md)). FT-03 is run by
hand ([§10.2](#102-failure-tests)).

Dependency rules:

- `cmd/*` packages build the dependencies and pass them to `internal`
  packages. No global state.
- `internal/event` imports no other internal package.
- Each consumer defines the small interfaces it needs (for example,
  `dlm.Catalog` or `dlm.SearchAdmin`), so it can be unit-tested with fakes.

## 3. Event schema

### 3.1 Fields

The same fields are used in OpenSearch and in Parquet. The OpenSearch index
template uses `dynamic: strict`.

| Field | OpenSearch type | Parquet type | Required | Description |
| --- | --- | --- | --- | --- |
| `event_id` | `keyword` | `VARCHAR` | Yes | 32-character hex ID ([§3.3](#33-event-id)); also the OpenSearch `_id` |
| `event_hash` | not stored | `UBIGINT` | Yes, Parquet only | First 8 bytes of the event ID digest; used for the fingerprint ([§6.3.3](#633-verification)) |
| `event_time` | `date` | `TIMESTAMPTZ` | Yes | When the event happened (UTC) |
| `observed_time` | `date` | `TIMESTAMPTZ` | Yes | When `ingest` processed the event |
| `partition_id` | `long` | `BIGINT` | Yes | Catalog partition |
| `source` | `keyword` | `VARCHAR` | Yes | `nslkdd` or `cloudtrail` |
| `event_type` | `keyword` | `VARCHAR` | Yes | `connection` or `api_call` |
| `category` | `keyword` | `VARCHAR` | Yes | NSL-KDD: `normal`, `DoS`, `Probe`, `R2L`, `U2R`. CloudTrail: `api_ok`, `api_error` |
| `severity` | `keyword` | `VARCHAR` | Yes | `INFO`, `WARN`, `ERROR`, or `CRITICAL` |
| `src_ip`, `dst_ip` | `ip` | `VARCHAR` | No | IP addresses |
| `protocol` | `keyword` | `VARCHAR` | No | `tcp`, `udp`, `icmp` |
| `service` | `keyword` | `VARCHAR` | No | NSL-KDD service, or CloudTrail `eventSource` |
| `bytes` | `long` | `BIGINT` | No | Bytes transferred |
| `duration_s` | `float` | `DOUBLE` | No | Connection duration in seconds |
| `label` | `keyword` | `VARCHAR` | No | NSL-KDD attack label |
| `event_name` | `keyword` | `VARCHAR` | No | CloudTrail `eventName` |
| `user_identity` | `keyword` | `VARCHAR` | No | CloudTrail `userIdentity.arn` |
| `aws_region` | `keyword` | `VARCHAR` | No | CloudTrail `awsRegion` |
| `error_code` | `keyword` | `VARCHAR` | No | CloudTrail `errorCode` |
| `raw_message` | `text` | `VARCHAR` | Yes | Original record. Indexed with the `raw_words` analyzer (split on `\W+`, lowercase; [ADR-0013](../adr/0013-shared-search-text-parser.md)). Indexed up to 8 KB; the full record stays in Bronze |
| `is_synthetic` | `boolean` | `BOOLEAN` | Yes | `true` if the timestamp or IP addresses were generated or shifted |
| `ingest_batch_id` | `long` | `BIGINT` | Yes | Batch that loaded the event |
| `schema_version` | `keyword` | `VARCHAR` | Yes | `1` |

### 3.2 Source mappings

**NSL-KDD.** `loadgen` writes an augmented CSV with no header: three generated
columns, `event_time` (RFC 3339), `src_ip`, and `dst_ip`, followed by the 43
original NSL-KDD columns.

| Source field | Target field | Rule |
| --- | --- | --- |
| Generated `event_time`, `src_ip`, `dst_ip` | Same names | Copy; set `is_synthetic` to `true` |
| `duration` | `duration_s` | Cast to float |
| `protocol_type` | `protocol` | Lowercase |
| `service` | `service` | Copy |
| `src_bytes` + `dst_bytes` | `bytes` | Sum |
| `label` | `label` | Copy |
| `label` | `category` | Lookup table, for example `neptune` → `DoS`; unknown labels → `normal` |
| `category` | `severity` | `normal` → `INFO`, `Probe` → `WARN`, `DoS` → `ERROR`, `R2L` and `U2R` → `CRITICAL` |
| The 43 original columns | `raw_message` | Joined with commas, unchanged |
| n/a | `event_type` | `connection` |

**CloudTrail.** Input files use the standard `{"Records": [...]}` structure.

| Source field | Target field | Rule |
| --- | --- | --- |
| `eventTime` | `event_time` | Parse ISO 8601 and convert to UTC. `loadgen` shifts times to recent dates and sets `is_synthetic` to `true` |
| `eventName` | `event_name` | Copy |
| `eventSource` | `service` | Copy |
| `userIdentity.arn` | `user_identity` | Copy; null if missing |
| `sourceIPAddress` | `src_ip` | Copy if it is an IP address; null if it is a service name such as `ec2.amazonaws.com` |
| `awsRegion` | `aws_region` | Copy |
| `errorCode` | `error_code`, `category`, `severity` | Present → `api_error` and `ERROR`; absent → `api_ok` and `INFO` |
| Whole record | `raw_message` | Compact JSON |
| n/a | `event_type` | `api_call` |

### 3.3 Event ID

```text
digest     = SHA-256(source + 0x1F + origin_id + 0x1F + decimal(offset))
event_id   = lowercase hex of digest[0:16]          (32 characters)
event_hash = big-endian uint64 of digest[0:8]
```

- `origin_id` is the SHA-256 of the input file, in hex.
- `offset` is the record's zero-based position in that file.

See [ADR-0011](../adr/0011-deterministic-event-id-from-origin-and-offset.md)
for why the ID is not a hash of the record content.

## 4. Partitioning and naming

### 4.1 Partitions

- A partition is one time bucket of event time, shared by all sources
  ([ADR-0001](../adr/0001-partition-by-time-bucket.md)).
- `bucket_start = floor(event_time in UTC, bucket_size)`. The bucket size
  comes from the active policy ([§7.2](#72-lifecycle-policies)).
- HOT and WARM index names are `logs-YYYY.MM.DD` for daily buckets and
  `logs-YYYY.MM.DD.HH` for hourly buckets.
- Partition states are `OPEN`, `SEALED`, and `DELETED`. Tiers are `HOT`,
  `WARM`, and `COLD`. A migration in progress is shown by an active row in
  `migration_jobs`, not by a separate state.

### 4.2 Late and out-of-range events

`ingest` rejects these events and records the reason in `rejected_records`:

| Reason | Condition |
| --- | --- |
| `LATE_EVENT` | The event's partition exists and is not `OPEN` |
| `FUTURE_EVENT` | `event_time` is more than 5 minutes after the current time |
| `EXPIRED_EVENT` | `event_time` is older than the policy's `retain_for` |

Backfills are not rejected as late. A partition is sealed only when its bucket
has ended **and** it has received no writes for `late_window`
([§6.3.1](#631-planner)).

### 4.3 Shard sizing

- When `ingest` creates an index, it sets the number of primary shards to
  `max(1, ceil(avg_daily_gb / target_shard_gb))`.
  - `avg_daily_gb` is the average size of the last 7 daily partitions.
  - `target_shard_gb` defaults to 25. OpenSearch recommends shards of
    10–50 GB.
- At 100 GB over 90 days (about 1.1 GB per day), each index has one primary
  shard. One index for all sources avoids the roughly 120 tiny shards that
  one index per source per day would create.
- The index template sets 0 replicas, because there is one local node. GKE
  would use 1, set by `OS_REPLICAS` ([§7.1](#71-environment-variables)).
- WARM indices are force-merged to one segment per shard.

### 4.4 COLD object layout

```text
bronze/source={source}/date={YYYY-MM-DD}/{file_sha256}.{csv|json}
cold/bucket={YYYYMMDDTHHZ}/job={job_id}/attempt={n}/part-{NNNNN}.parquet
cold/bucket={YYYYMMDDTHHZ}/job={job_id}/attempt={n}/_manifest.json
```

- Parquet files are sorted by `event_time`, compressed with ZSTD, and limited
  to about 256 MB each. DuckDB writes bloom filters automatically for
  dictionary-encoded columns.
- The manifest is written last
  ([ADR-0006](../adr/0006-manifest-last-commit-for-cold-writes.md)).
  `fingerprint` is a decimal string, because unsigned 64-bit values exceed the
  safe integer range of JSON.

```json
{
  "manifest_version": 1,
  "partition_id": 123,
  "bucket_start": "2026-11-09T00:00:00Z",
  "bucket_end": "2026-11-10T00:00:00Z",
  "job_id": 42,
  "attempt": 1,
  "row_count": 1048576,
  "fingerprint": "9223372036854775817",
  "min_event_time": "2026-11-09T00:00:01Z",
  "max_event_time": "2026-11-09T23:59:58Z",
  "files": [
    { "key": "part-00000.parquet", "rows": 1048576, "bytes": 134217728, "sha256": "…" }
  ],
  "created_at": "2026-12-09T01:00:00Z"
}
```

## 5. Database schema

All timestamps are `TIMESTAMPTZ` in UTC. Tables are created by numbered
migrations in `migrations/`.

### 5.1 obs_meta

**`log_sources`**

| Column | Type | Notes |
| --- | --- | --- |
| `source_id` | `SMALLINT` PK | |
| `source_name` | `VARCHAR(20)` UNIQUE | `nslkdd`, `cloudtrail` |
| `format` | `VARCHAR(10)` | `csv`, `json` |
| `is_active` | `BOOLEAN` | |

**`tier_policies`**

| Column | Type | Notes |
| --- | --- | --- |
| `policy_name` | `VARCHAR(20)` PK | `default`, `demo` |
| `bucket_size` | `INTERVAL` | |
| `hot_after` | `INTERVAL` | Age at which data moves to WARM |
| `warm_after` | `INTERVAL` | Age at which data moves to COLD |
| `retain_for` | `INTERVAL` | Age at which data is deleted |
| `late_window` | `INTERVAL` | Quiet period before sealing |
| `purge_grace` | `INTERVAL` | Wait before an old copy is deleted; must exceed the COLD query timeout |
| `target_shard_gb` | `INT` | |
| `is_active` | `BOOLEAN` | Exactly one active row, enforced by a partial unique index |

Age is measured as `now() - bucket_end`.

**`data_partitions`** (the catalog)

| Column | Type | Notes |
| --- | --- | --- |
| `partition_id` | `BIGSERIAL` PK | |
| `bucket_start` | `TIMESTAMPTZ` UNIQUE | |
| `bucket_end` | `TIMESTAMPTZ` | |
| `tier` | `VARCHAR(4)` | `HOT`, `WARM`, `COLD` |
| `state` | `VARCHAR(7)` | `OPEN`, `SEALED`, `DELETED` |
| `location` | `TEXT` | Index name, or manifest URI for COLD |
| `doc_count` | `BIGINT` | While `OPEN`: refreshed by `dlm` every plan cycle for the dashboards. Set exactly when sealed |
| `fingerprint` | `NUMERIC(20,0)` | Set when the partition reaches COLD |
| `size_bytes` | `BIGINT` | Set by `dlm`: refreshed while `OPEN`, recorded at seal and after each move |
| `last_write_at` | `TIMESTAMPTZ` | Updated by `ingest`, at most every 10 s per partition |
| `sealed_at` | `TIMESTAMPTZ` | |
| `gold_final_at` | `TIMESTAMPTZ` | Set by `dlm` after the final hourly rollup ([§6.5](#65-hourly-rollup)) |
| `version` | `INT` | Optimistic concurrency check for tier switches |
| `created_at`, `updated_at` | `TIMESTAMPTZ` | |

**`migration_jobs`**

| Column | Type | Notes |
| --- | --- | --- |
| `job_id` | `BIGSERIAL` PK | |
| `partition_id` | `BIGINT` FK | |
| `job_type` | `VARCHAR(12)` | `HOT_TO_WARM`, `WARM_TO_COLD`, `DELETE` |
| `status` | `VARCHAR(14)` | `PENDING`, `COPYING`, `VERIFYING`, `SWITCHING`, `PURGE_PENDING`, `ROLLING_BACK`, `DONE`, `FAILED` |
| `attempt` | `SMALLINT` | Starts at 0 |
| `max_attempts` | `SMALLINT` | Default 3 |
| `staging_uri` | `TEXT` | Attempt prefix for WARM → COLD |
| `src_count`, `dst_count` | `BIGINT` | |
| `src_fingerprint`, `dst_fingerprint` | `NUMERIC(20,0)` | |
| `purge_after` | `TIMESTAMPTZ` | |
| `next_run_at` | `TIMESTAMPTZ` | For retry backoff |
| `error_msg` | `TEXT` | Last error |
| `created_at`, `started_at`, `finished_at` | `TIMESTAMPTZ` | |

`CREATE UNIQUE INDEX ux_active_job ON migration_jobs (partition_id) WHERE status NOT IN ('DONE', 'FAILED');`
enforces INV-6.

**`ingest_batches`**

| Column | Type | Notes |
| --- | --- | --- |
| `batch_id` | `BIGSERIAL` PK | |
| `source_id` | `SMALLINT` FK | |
| `file_name` | `TEXT` | |
| `file_sha256` | `CHAR(64)` | Indexed; the same file may appear more than once |
| `bronze_uri` | `TEXT` | |
| `rows_read`, `rows_loaded`, `rows_rejected`, `rows_duplicate` | `BIGINT` | |
| `status` | `VARCHAR(8)` | `RUNNING`, `SUCCESS`, `PARTIAL`, `FAILED` |
| `started_at`, `finished_at` | `TIMESTAMPTZ` | |

**`rejected_records`**

| Column | Type | Notes |
| --- | --- | --- |
| `reject_id` | `BIGSERIAL` PK | |
| `batch_id` | `BIGINT` FK | |
| `record_offset` | `BIGINT` | |
| `reason` | `VARCHAR(40)` | Rule name, for example `PARSE_ERROR` or `LATE_EVENT` |
| `detail` | `TEXT` | |
| `raw_record` | `TEXT` | First 2 KB |
| `rejected_at` | `TIMESTAMPTZ` | |

**`query_audit`**

| Column | Type | Notes |
| --- | --- | --- |
| `query_id` | `UUID` PK | |
| `requested_at` | `TIMESTAMPTZ` | |
| `query_text` | `VARCHAR(200)` | |
| `time_from`, `time_to` | `TIMESTAMPTZ` | |
| `tiers` | `TEXT[]` | Tiers searched |
| `partitions_planned`, `partitions_failed` | `INT` | |
| `result_count` | `INT` | |
| `duration_ms` | `INT` | End to end |
| `tier_durations` | `JSONB` | For example `{"HOT": 41, "COLD": 790}` |
| `status` | `VARCHAR(8)` | `COMPLETE`, `PARTIAL`, `FAILED` |

**`tier_prices`** (migration `0003`)

| Column | Type | Notes |
| --- | --- | --- |
| `tier` | `VARCHAR(4)` PK | `HOT`, `WARM`, `COLD` |
| `usd_per_gb_month` | `NUMERIC(8,4)` | Approximate Google Cloud list price, us-central1 |
| `product` | `TEXT` | Product the price is for, such as `Cloud Storage Nearline` |

The storage and cost dashboard (RPT-04) multiplies each tier's `size_bytes`
by its price. Prices are data, like policies, so they can be updated with a
migration rather than a redeploy.

### 5.2 obs_gold

`obs_gold` is a schema in the same database as `obs_meta`, created by
migration `0004`. `dlm` writes it ([§6.5](#65-hourly-rollup)).

**`fact_events_hourly`** has the primary key (`hour_start`, `source`,
`category`).

| Column | Type | Notes |
| --- | --- | --- |
| `hour_start` | `TIMESTAMPTZ` | |
| `source` | `VARCHAR(20)` | |
| `category` | `VARCHAR(20)` | |
| `event_count` | `BIGINT` | |
| `error_count` | `BIGINT` | Events with severity `ERROR` or `CRITICAL` |
| `bytes_total` | `BIGINT` | |

**`events_hourly_scored`** is a view over `fact_events_hourly` that adds the
anomaly flag (FR-07):

```sql
avg(event_count)          OVER w AS mu,
stddev_samp(event_count)  OVER w AS sigma,
count(*)                  OVER w AS n
WINDOW w AS (PARTITION BY source, category ORDER BY hour_start
             ROWS BETWEEN 24 PRECEDING AND 1 PRECEDING)
```

| Column | Notes |
| --- | --- |
| `baseline` | `mu`, the average of the previous 24 rows |
| `anomaly_score` | `(event_count − mu) / greatest(sigma, 1)`; null while `n < 12` |
| `is_anomaly` | `n ≥ 12`, `abs(score) ≥ 3`, and `greatest(event_count, mu) ≥ 100` |

Hours with no events have no row, so a source that goes completely silent is
not flagged.

Storage cost per tier (RPT-04) is computed when the dashboard is read, from
`data_partitions.size_bytes` and `tier_prices` ([§5.1](#51-obs_meta)).

## 6. Service design

### 6.1 loadgen

```text
loadgen --source nslkdd|cloudtrail --input PATH --out DIR
        [--backfill 6h] [--target-gb N] [--live --rate EPS] [--seed N]
```

| Behaviour | Detail |
| --- | --- |
| NSL-KDD | Replays rows in a random order fixed by `--seed`. Spreads `event_time` evenly over the backfill window. Generates `src_ip` from 10.0.0.0/8 and `dst_ip` from 172.16.0.0/12 |
| CloudTrail | Shifts all `eventTime` values by one constant offset so the newest event is at the current time. Repeats the dataset with further offsets to reach `--target-gb` |
| Output | About 100 MB files. Each is written as `*.tmp` and renamed when complete, so `ingest` never reads a partial file |
| Live mode | Writes one file every 10 s at `--rate` events per second |

### 6.2 ingest

**Inbox.** `ingest` checks `INGEST_INBOX` every 5 s for files not ending in
`.tmp`. It processes up to 2 files at a time, then moves each one to `done/`,
or to `failed/` if it cannot be read.

**Pipeline for each file:**

1. Insert an `ingest_batches` row with status `RUNNING`.
2. **First pass.** Compute the file's SHA-256, which becomes the `origin_id`.
3. **Second pass.** Read the file through an `io.TeeReader`. The raw bytes
   stream to Bronze while the records are parsed.
4. **Scanner.** Split records with `bufio.Scanner` for CSV, or
   `json.Decoder` over the `Records` array for CloudTrail. Records larger than
   `INGEST_MAX_RECORD_BYTES` are rejected. Records go to a channel with a
   capacity of 10,000.
5. **Workers.** `INGEST_WORKERS` workers (default `GOMAXPROCS`) map and
   validate each record, then compute `event_id` and `bucket_start`. Events go
   to a channel with a capacity of 10,000.
6. **Partition resolver.** A single goroutine with a cache. For a new bucket,
   it inserts the catalog row (`INSERT … ON CONFLICT DO NOTHING`) and then
   creates the index from the index template. It rejects events for
   partitions that are not `OPEN` with `LATE_EVENT`. It updates
   `last_write_at` at most every 10 s for each partition.
7. **Bulk writers.** `INGEST_BULK_WORKERS` writers (default 2) send `_bulk`
   requests with `op_type=create` and `_id = event_id`. They flush at 5 MB or
   every 1 s, whichever comes first.
8. Update the batch counters and status.

**Validation rules.**

| Rule | Action |
| --- | --- |
| `PARSE_ERROR`: malformed CSV or JSON | Reject |
| `RECORD_TOO_LARGE` | Reject |
| `MISSING_FIELD`: no `event_time` or `source` | Reject |
| `INVALID_TIMESTAMP` | Reject |
| `FUTURE_EVENT`, `EXPIRED_EVENT`, `LATE_EVENT` | Reject ([§4.2](#42-late-and-out-of-range-events)) |
| Invalid IP, port outside 0–65535, negative bytes | Set the field to null, keep the event, and increment `ingest_field_nulled_total{field}` |

**Bulk responses.**

| Response | Action |
| --- | --- |
| 201 created | Count as loaded |
| 409 conflict | Already indexed: count as a duplicate (INV-5) |
| 429 or `es_rejected_execution` | Retry only the failed items with exponential backoff and jitter, up to 5 attempts |
| 400 mapping error | Reject with `MAPPING_ERROR` |
| 5xx or connection error | Retry the whole request up to 5 times, then mark the batch `PARTIAL` |

**Batch status.** `SUCCESS` if no more than 1 % of rows were rejected,
`PARTIAL` if more were rejected or retries ran out, and `FAILED` if the file
could not be read.

**Metrics:** `ingest_events_total{source,outcome}`,
`ingest_bulk_duration_seconds`, `ingest_queue_depth{stage}`,
`ingest_freshness_seconds`, `ingest_field_nulled_total{field}`.

### 6.3 dlm

`dlm` runs as a single replica
([ADR-0005](../adr/0005-resumable-single-worker-migration-jobs.md)). It holds
the session advisory lock `pg_try_advisory_lock(hashtext('obs.dlm'))` while it
works. A second instance waits and retries every 10 s.

#### 6.3.1 Planner

The planner runs every `DLM_PLAN_INTERVAL` (default 30 s).

| Rule | Condition | Action |
| --- | --- | --- |
| Seal | `state = OPEN`, `now ≥ bucket_end + late_window`, and `now ≥ last_write_at + late_window` | Set `index.blocks.write=true`, refresh, then record `doc_count`, `state = SEALED`, and `sealed_at` |
| HOT → WARM | `tier = HOT`, `state = SEALED`, and age ≥ `hot_after` | Insert a `HOT_TO_WARM` job |
| WARM → COLD | `tier = WARM`, age ≥ `warm_after`, and `gold_final_at` is set | Insert a `WARM_TO_COLD` job |
| Delete | `state ≠ DELETED` and age ≥ `retain_for` | Insert a `DELETE` job |

Each partition matches at most one rule, and Delete wins over a move. Jobs
are inserted with `ON CONFLICT DO NOTHING`, so the partial unique index
prevents a second active job for the same partition. A partition whose last
job of that type is `FAILED` gets no new job until someone investigates.

#### 6.3.2 Job steps

The runner checks every `DLM_RUN_INTERVAL` (default 5 s). It runs the oldest
job that is not `DONE` or `FAILED` and whose `next_run_at` has passed, one job
at a time. Each status change is committed before the next step starts.

**`HOT_TO_WARM`**

| Status | Work | Next status |
| --- | --- | --- |
| `PENDING` | Increment `attempt` | `COPYING` |
| `COPYING` | Force-merge to one segment; set replicas to 0; on GKE, set `index.routing.allocation.require.temp=warm`; wait for green health (timeout 10 min) | `VERIFYING` |
| `VERIFYING` | `_count` equals `doc_count` | `SWITCHING` |
| `SWITCHING` | Update `tier = WARM` and `version = version + 1`, where `version` and `tier = HOT` still match | `DONE` |

**`WARM_TO_COLD`**

| Status | Work | Next status |
| --- | --- | --- |
| `PENDING` | Increment `attempt`; set `staging_uri` to the attempt prefix | `COPYING` |
| `COPYING` | Delete any leftover files at the attempt prefix and in the spool. Open a PIT (`keep_alive` 5 min). Page through it with `search_after`, sorted by `event_time` then `event_id`, 10,000 documents per page. Compute `src_count` and `src_fingerprint`. Append rows to a DuckDB table in `DLM_SPOOL_DIR`. Write sorted Parquet files to the spool, compute each file's SHA-256, upload the files, and upload the manifest last | `VERIFYING` |
| `VERIFYING` | [§6.3.3](#633-verification) | `SWITCHING`, or `ROLLING_BACK` on mismatch |
| `SWITCHING` | Update `tier = COLD`, `location` (manifest URI), `fingerprint`, `size_bytes`, and `version`, with the version check. Set `purge_after = now + purge_grace` | `PURGE_PENDING` |
| `PURGE_PENDING` | After `purge_after`, delete the WARM index. A missing index counts as success | `DONE` |
| `ROLLING_BACK` | Delete the attempt prefix and the spool. A missing object counts as success | `PENDING` with backoff, or `FAILED` |

**`DELETE`**

| Status | Work | Next status |
| --- | --- | --- |
| `PENDING` | Increment `attempt` | `SWITCHING` |
| `SWITCHING` | Set `state = DELETED` and increment `version`. The router stops reading the partition | `PURGE_PENDING` |
| `PURGE_PENDING` | After `purge_after`, delete the index or the partition's COLD prefix | `DONE` |

#### 6.3.3 Verification

- **Source side.** While reading from the PIT, `dlm` counts the documents and
  computes the fingerprint `fp = Σ event_hash mod 2^64`. In Go this is
  unsigned addition on a `uint64`, which wraps around at 2^64.
- **Target side.** `dlm` reads the uploaded files back from the object store:

  ```sql
  SELECT count(*), sum(event_hash) % 18446744073709551616
  FROM   read_parquet([<file URIs from the manifest>]);
  ```

- **Pass condition.** `doc_count = src_count = dst_count`,
  `src_fingerprint = dst_fingerprint`, and each object's size matches the
  manifest.
- A sum is used instead of XOR because a sum also detects a duplicated row.

#### 6.3.4 Recovery and retries

- On startup, the runner continues each unfinished job from its saved status.
  Every step can be repeated safely (INV-7):
  - `COPYING` cleans up and starts its copy again.
  - `VERIFYING` only reads.
  - `SWITCHING` treats "already switched to this location" as success.
  - Deletions treat "already gone" as success.
- A failure in `COPYING` or `VERIFYING` leads to `ROLLING_BACK`. A
  `HOT_TO_WARM` rollback removes the allocation setting.
- Retries wait 2^`attempt` minutes. After `max_attempts`, the job becomes
  `FAILED`, which raises an alert in RPT-01.
- A version conflict in `SWITCHING` sets the job to `FAILED` without a retry,
  because it means the partition changed unexpectedly.
- On `SIGTERM`, `dlm` stops starting new steps, cancels the current step
  (which runs again after restart), and releases the lock.

**Metrics:** `dlm_jobs_total{type,result}`,
`dlm_step_duration_seconds{type,step}`, `dlm_partitions{tier,state}`,
`dlm_bytes{tier}`, `dlm_lock_held`.

### 6.4 router

#### 6.4.1 API

`GET /api/v1/search`

| Parameter | Required | Rules |
| --- | --- | --- |
| `from`, `to` | Yes | RFC 3339; `to` must be later than `from` |
| `q` | No | Up to 200 characters. `a b` (both), `a \| b` (either), `-a` (not), `"a b"` (adjacent words), `ab*` (prefix). Other syntax returns `400` ([ADR-0013](../adr/0013-shared-search-text-parser.md)) |
| `event_id` | No | Exact match |
| `source` | No | Can be repeated: `nslkdd`, `cloudtrail` |
| `severity` | No | Can be repeated |
| `limit` | No | 1–1000, default 100 |

Response (200):

```json
{
  "query_id": "6f1c0c1e-0d3a-4b8e-9a51-2f6a1d5b7c42",
  "status": "PARTIAL",
  "took_ms": 812,
  "tiers": [
    { "tier": "HOT", "partitions": 1, "took_ms": 41, "hits": 100, "status": "OK" },
    { "tier": "COLD", "partitions": 3, "took_ms": 790, "hits": 0, "status": "TIMEOUT" }
  ],
  "hits": [
    {
      "event_id": "…",
      "event_time": "…",
      "tier": "HOT",
      "source": "nslkdd",
      "severity": "ERROR",
      "raw_message": "…"
    }
  ]
}
```

Errors use RFC 9457 problem details (`application/problem+json`):

| Status | When |
| --- | --- |
| 400 | Invalid parameters, or a COLD time span wider than `ROUTER_MAX_COLD_WINDOW` |
| 503 | Every tier failed, or the catalog is unavailable and nothing is cached |

#### 6.4.2 Planning and execution

1. **Plan.** Select partitions where `state <> 'DELETED'`,
   `bucket_start < to`, and `bucket_end > from`. The catalog is cached for
   `ROUTER_CATALOG_CACHE_TTL` (default 5 s).
2. **HOT and WARM.** One OpenSearch `_search` over the planned indices, with
   `ignore_unavailable=true`:
   - `bool.filter`: a range on `event_time`, plus `terms` on `source` and
     `severity`.
   - `bool.must` and `bool.must_not`: built from the parsed search text, with
     `match_phrase` or `prefix` on `raw_message` for each term and
     `bool.should` for `|` groups
     ([ADR-0013](../adr/0013-shared-search-text-parser.md)).
   - Sorted by `event_time` descending, with `size = limit` and a timeout of
     `ROUTER_HOT_TIMEOUT` (default 5 s).
3. **COLD.** Read each manifest; manifests are cached by URI because they
   never change. Run one DuckDB query with a timeout of `ROUTER_COLD_TIMEOUT`
   (default 20 s):

   ```sql
   SELECT event_id, event_time, source, severity, raw_message
   FROM   read_parquet([<manifest file URIs>])
   WHERE  event_time >= ? AND event_time < ?
     AND  regexp_matches(lower(raw_message), ?)   -- one per clause; NOT for -a
   ORDER  BY event_time DESC, event_id
   LIMIT  ?;
   ```

   - Each pattern joins the clause's words with `\W+` between `\b`
     anchors, with `|` alternatives in one group, and is passed as a bound
     parameter ([ADR-0013](../adr/0013-shared-search-text-parser.md)).
   - File URIs come from manifests, not from the request. They are checked
     against the COLD prefix and quoted.
4. **Merge.** Combine the results, sort by `event_time` descending, remove
   duplicates by `event_id`, and cut to `limit`. The status is `COMPLETE` if
   every tier succeeded, otherwise `PARTIAL`.
5. **Audit.** Send the audit row to a buffered channel (capacity 1,000). One
   goroutine inserts the rows. If the channel is full, the row is dropped and
   `router_audit_dropped_total` is incremented.

#### 6.4.3 DuckDB pool

- One in-memory DuckDB database is opened through `database/sql`, with
  `SetMaxOpenConns(ROUTER_COLD_CONCURRENCY)` (default 4). Each query
  therefore runs on its own connection.
- On startup, the router configures the database once:
  - `LOAD httpfs`
  - `CREATE SECRET`: type `s3` with a path-style endpoint for SeaweedFS, or
    type `gcs` with HMAC keys on GKE
  - `SET memory_limit = '512MB'`
  - `SET threads = 2`

**Metrics:** `router_requests_total{status}`,
`router_tier_duration_seconds{tier}`, `router_tier_errors_total{tier,reason}`,
`router_audit_dropped_total`.

### 6.5 Hourly rollup

`dlm` keeps `obs_gold.fact_events_hourly` up to date in every plan cycle,
after sealing and before creating jobs
([ADR-0014](../adr/0014-hourly-rollups-in-dlm.md)).

1. Select HOT and WARM partitions that are `OPEN`, or `SEALED` with
   `gold_final_at IS NULL`.
2. For each, run one OpenSearch query with `size: 0`: a `date_histogram` on
   `event_time` (1 hour, UTC), `terms` on `source` and then `category`, a
   `sum` of `bytes`, and a `filter` count of `ERROR` and `CRITICAL` events.
   One shard per index makes the counts exact.
3. In one transaction, delete the partition's hours from
   `fact_events_hourly`, insert the new rows, and, if the partition is
   `SEALED`, set `gold_final_at`. Partitions cover whole hours, so no hour
   belongs to two partitions.

A failed rollup is logged and retried in the next cycle. A partition moves to
COLD only after `gold_final_at` is set ([§6.3.1](#631-planner)), because COLD
has no index to aggregate.

## 7. Configuration

### 7.1 Environment variables

Configuration comes from environment variables. Secrets are never put in
images or committed files. A service stops at start-up if a required
variable is missing or a value is invalid.

| Variable | Used by | Default |
| --- | --- | --- |
| `LOG_LEVEL` | All | `info` |
| `HTTP_ADDR` | All | `:8080` |
| `PG_DSN` | All except `loadgen` | Required |
| `OS_URL` | `ingest`, `dlm`, `router` | Required |
| `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | `ingest`, `dlm`, `router` | Required |
| `S3_REGION` | `ingest`, `dlm`, `router` | `us-east-1` |
| `S3_USE_PATH_STYLE` | `ingest`, `dlm`, `router` | `true` |
| `INGEST_INBOX` | `ingest` | `/data/inbox` |
| `INGEST_WORKERS` | `ingest` | `GOMAXPROCS` |
| `INGEST_BULK_WORKERS` | `ingest` | `2` |
| `DLM_PLAN_INTERVAL` | `dlm` | `30s` |
| `DLM_RUN_INTERVAL` | `dlm` | `5s` |
| `DLM_SPOOL_DIR` | `dlm` | `/spool` |
| `DLM_TEST_HOOK` | `dlm` | Empty. `delete_part_before_verify` enables the FT-02 fault |
| `ROUTER_HOT_TIMEOUT` | `router` | `5s` |
| `ROUTER_COLD_TIMEOUT` | `router` | `20s` |
| `ROUTER_COLD_CONCURRENCY` | `router` | `4` |
| `ROUTER_MAX_COLD_WINDOW` | `router` | `168h` |
| `ROUTER_CATALOG_CACHE_TTL` | `router` | `5s` |
| `DLM_DUCKDB_MEMORY` | `dlm` | `256MB` for each DuckDB database (export and read-back) |
| `ROUTER_DUCKDB_MEMORY` | `router` | `512MB` |

Docker Compose passes each of these from `.env`; a value left empty keeps
the default above, so defaults are defined only in the code. `HTTP_ADDR`,
`INGEST_INBOX`, `DLM_SPOOL_DIR` and the connection settings are fixed by
`compose.yaml`, because they must match its ports, volumes and service names.
`.env` also sets the host ports (`ROUTER_PORT`, `GRAFANA_PORT`, and so on)
and OpenSearch's heap and memory limit (`OPENSEARCH_HEAP`,
`OPENSEARCH_MEMORY`). `.env.example` lists every optional setting.

`loadgen` takes command-line flags instead (`-target-gb`, `-backfill`,
`-file-mb`, `-seed`, `-input`, `-out`).

**Fixed limits.** These values are constants in the code, not settings. They
protect correctness or define the API, and none has needed tuning. A constant
becomes a variable only when a deployment needs a different value.

| Limit | Value | Where |
| --- | --- | --- |
| Inbox poll interval; files loaded at the same time | 5 s; 2 | `internal/ingest` |
| Pipeline queue size; largest record | 10,000 records; 64 KB | `internal/ingest` |
| Bulk request size; flush interval; attempts | 5 MB; 1 s; 5 | `internal/ingest` |
| Event time accepted ahead of the clock | 5 min | `internal/ingest` |
| Reject share above which a batch is `PARTIAL` | 1 % | `internal/ingest` |
| Bronze upload timeout | 5 min | `internal/ingest` |
| Shards and replicas per index | 1 and 0 | `internal/search/template.json` |
| PIT page size; PIT keep-alive | 10,000 documents; 5 min | `internal/dlm` |
| Force-merge timeout | 10 min | `internal/dlm` |
| Parquet file size | 256 MB | `internal/dlm` |
| DuckDB threads | 2 | `internal/cold` |
| Search text length; result limit | 200 characters; 1–1,000, default 100 | `internal/router` ([§6.4.1](#641-api)) |
| Query audit queue | 1,000 rows | `internal/router` |

**Planned for the GKE stretch goal, not built:** `OS_USERNAME` and
`OS_PASSWORD` (the local OpenSearch runs without security), `OS_REPLICAS`
(1 on GKE), `OS_WARM_ALLOCATION` (move WARM indices to standard-disk nodes),
and `DEBUG_ENDPOINTS` (`/debug/hang` for FT-04).

### 7.2 Lifecycle policies

Policies are rows in `tier_policies`, created by a migration
([ADR-0007](../adr/0007-interval-based-lifecycle-policies.md)).
`make demo` activates the `demo` policy.

| Setting | `default` | `demo` |
| --- | --- | --- |
| `bucket_size` | 1 day | 1 hour |
| `hot_after` | 7 days | 1 hour |
| `warm_after` | 30 days | 3 hours |
| `retain_for` | 365 days | 24 hours |
| `late_window` | 2 hours | 2 minutes |
| `purge_grace` | 1 hour | 5 minutes |
| `target_shard_gb` | 25 | 25 |

## 8. Health endpoints and probes

| Endpoint | Behaviour |
| --- | --- |
| `/healthz` | Liveness. Each long-running loop updates a heartbeat on every tick, including when idle. Returns 500 if any heartbeat is older than 2 minutes. Does not check dependencies ([ADR-0012](../adr/0012-watchdog-based-liveness-probe.md)) |
| `/readyz` | Readiness. Checks the service's dependencies: PostgreSQL ping, OpenSearch health not red, and object store `HeadBucket`. A standby `dlm` that does not hold the lock reports not ready |
| `/metrics` | Prometheus metrics, including Go runtime metrics |
| `/debug/hang` | Blocks the main loop, for FT-04. Available only when `DEBUG_ENDPOINTS=true` |

Kubernetes probes (stretch goal):

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8080 }
  periodSeconds: 10
  failureThreshold: 3
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 5
  failureThreshold: 2
startupProbe:
  httpGet: { path: /healthz, port: 8080 }
  periodSeconds: 5
  failureThreshold: 12
```

### 8.1 Dashboards

Grafana loads three dashboards from `deploy/grafana/dashboards` at start-up.
They cover the reports in [SRS-001 §5](requirements.md#5-reports):

| Report | Dashboard | Panels | Source |
| --- | --- | --- | --- |
| RPT-01 System health | System health | Services, ingest rate, reject rate, DLM status, failed jobs, ingest and search throughput, CPU and memory by service, search time p95 by tier | Prometheus; `migration_jobs` |
| RPT-02 Data volume and quality | System health | Events loaded per hour by source, recent ingest batches, rejected records by reason | `ingest_batches`, `rejected_records` |
| RPT-03 Security events and anomalies | Security events | Events, error events, error share, anomalous hours, events per hour by category, error events per hour by source, totals by source and category, anomalous-hours table | `obs_gold.fact_events_hourly`, `obs_gold.events_hourly_scored` |
| RPT-04 Storage tier and cost | Storage and search | Estimated monthly cost, all-HOT cost, savings, savings rate, by-tier table, size by tier, partitions | `data_partitions`, `tier_prices` |
| RPT-05 Search performance | Storage and search | Search time p95 by tier over time, p50 and p95 by tier, search results by status | `query_audit` |
| RPT-06 Lifecycle audit | System health | Migration jobs table, failed jobs | `migration_jobs` |

CPU and memory come from each Go service's process metrics
(`process_cpu_seconds_total`, `process_resident_memory_bytes`).

## 9. Error handling and logging

- Functions return errors wrapped with context, for example
  `fmt.Errorf("seal partition %d: %w", id, err)`. Only `main` exits on an
  error.
- Every function that does I/O takes a `context.Context` as its first
  argument. Shutdown cancels the root context.
- Each pipeline worker recovers from panics, logs the record offset, and
  rejects the record.
- Logs are JSON, written with `log/slog`. Every line includes `time`,
  `level`, `msg`, `service`, and `version`. Lines also include `batch_id`,
  `job_id`, `partition_id`, or `query_id` where relevant.
- Secrets and full raw records are never logged at `info` level or above.

## 10. Test design

### 10.1 Unit and integration tests

| Level | Scope | Tooling |
| --- | --- | --- |
| Unit | Source mappers, validation rules, event ID, fingerprint, planner rules, anomaly score, result merge | `go test`, table-driven tests |
| Integration | Catalog transactions, job state machine against real OpenSearch, PostgreSQL, and SeaweedFS | `go test` with Testcontainers |
| End to end | FT-01 to FT-04 | Docker Compose and scripts in `test/e2e` |

CI runs `go vet`, `staticcheck`, and the unit and integration tests on every
pull request.

### 10.2 Failure tests

During each test, a search loop queries one known `event_id` from the
affected partition every second. A test passes only if every search finds
that event (FR-04).

| ID | Fault | Pass criteria |
| --- | --- | --- |
| FT-01 | `SIGKILL` to the `dlm` process while a `WARM_TO_COLD` job is `COPYING` (`make test-failure-dlm-crash`) | Docker restarts `dlm`; the job reaches `DONE`; `dst_count = doc_count`; no search fails |
| FT-02 | A test hook (`DLM_TEST_HOOK=delete_part_before_verify`) deletes one Parquet file before `VERIFYING` on the first attempt (`make test-failure-lost-cold-file`) | The job rolls back, retries, and reaches `DONE`; the catalog never points to the broken manifest; no search fails |
| FT-03 | The same input file is ingested twice | The OpenSearch document count is unchanged; the second batch has `rows_duplicate = rows_read` |
| FT-04 (stretch) | On GKE, `/debug/hang` is called on `dlm` | The liveness probe fails; Kubernetes restarts the pod within 2 minutes; the job resumes |

`test/failure/run.py` runs FT-01 and FT-02 against the local stack and
restores the active policy afterwards. The services run under `tini`
(`init: true` in Compose), so a killed `dlm` process exits its container and
Docker's restart policy brings it back.

### 10.3 Hot versus cold benchmark

| ID | Query |
| --- | --- |
| BQ-1 | `event_id` exact match |
| BQ-2 | One term in `raw_message` over the partition's time range |
| BQ-3 | `source` and `severity` filters over the partition's time range |

Procedure:

1. Pick one partition.
2. While it is in HOT, run each query 5 times to warm up, then 30 measured
   times.
3. Let the partition move to COLD, then repeat step 2.
4. Repeat at about 1 GB and 10 GB of total data.
5. Report p50 and p95 latency per tier from `query_audit.tier_durations`
   (RPT-05).

### 10.4 Resource profiles

CPU and memory of each Go service are collected by Prometheus from the
services' own process metrics (`process_cpu_seconds_total`,
`process_resident_memory_bytes`) under three load profiles. cAdvisor was
planned for container metrics, but on Docker Desktop it cannot resolve
container names.

| Profile | Load |
| --- | --- |
| Idle | Stack running, no load |
| Ingest | `loadgen` at its maximum sustained rate |
| Ingest and migration | Ingest plus an active `WARM_TO_COLD` job |

For each profile, the report shows mean and p95 CPU, mean and p95 memory, and
Go garbage-collection pause time (`go_gc_duration_seconds`) (SQ-2).

## 11. Validation spikes

| ID | Spike | Pass condition | Before |
| --- | --- | --- | --- |
| SP-1 | Build `duckdb-go` in the Docker image for arm64 and amd64 | `SELECT 42` runs in containers on both architectures | M0 |
| SP-2 | DuckDB reads Parquet from SeaweedFS and skips row groups | `EXPLAIN ANALYZE` shows skipped row groups for a time-range query on a sorted 1 GB file | M4 |
| SP-3 | PIT export of 1 million documents to Parquet | `dlm` stays under its 512 MB memory limit | M4 |
| SP-4 | The AWS SDK and DuckDB both work with GCS through HMAC keys | The same code paths pass against GCS | M6 |

## 12. Revision history

| Version | Date | Author | Change |
| --- | --- | --- | --- |
| 1.0 | 2026-11-09 | Group 6 | Baseline |
| 1.1 | 2026-11-26 | Group 6 | Search text syntax and analyzer (§3.1, §6.4.1, §6.4.2) per ADR-0013 |
| 1.2 | 2026-11-26 | Group 6 | WARM → COLD no longer waits for `gold_final_at` (no aggregator); planner rule precedence (§6.3.1); `DLM_TEST_HOOK` (§7.1); FT-01 and FT-02 runners (§10.2) |
| 1.3 | 2026-11-26 | Group 6 | `tier_prices` table; `dlm` keeps `doc_count` and `size_bytes` current for open partitions (§5.1) |
| 1.4 | 2026-11-26 | Group 6 | Hourly rollup in `dlm` instead of an `aggregator` service, per ADR-0014 (§5.2, §6.5, §7.1); WARM → COLD waits for `gold_final_at` again (§6.3.1) |
| 1.5 | 2026-11-27 | Group 6 | Glossary link (§1); report-to-dashboard mapping (§8.1) |
| 1.6 | 2026-11-27 | Group 6 | Code-structure tree matches the repository (§2); resource profiles use process metrics instead of cAdvisor (§10.4) |
| 1.7 | 2026-11-27 | Group 6 | §7.1 lists only the variables the code reads; ingest bulk, flush and record limits are fixed constants; GKE-only variables marked as planned (§4.3, §7.1) |
| 1.8 | 2026-11-27 | Group 6 | `DLM_DUCKDB_MEMORY` and `ROUTER_DUCKDB_MEMORY`; Compose passes service settings, host ports and OpenSearch memory from `.env` (§7.1) |
