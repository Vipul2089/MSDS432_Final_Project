# Observability Fabric

Searchable, tiered log storage with an automated Data Lifecycle Management
(DLM) service written in Go.

Course project for MSDS 432, Foundations of Data and AI Engineering (Fall
2026), Project 6, "Predictive IT Operations", by Group 6.

## Status

Requirements and architecture are baselined at v1.0 (2026-11-09), and the
Phase 3 implementation runs locally with Docker Compose. Working today:
generating and loading NSL-KDD log files, search across all three storage
tiers, automatic moves from HOT to WARM to COLD and deletion after retention,
three Grafana dashboards, failure tests and a scripted demo. See the
[milestones](docs/design/hld.md#12-milestones) in the high-level design.

## Documentation

| Document | Purpose |
| --- | --- |
| [Requirements](docs/design/requirements.md) | Functional and non-functional requirements, reports, traceability |
| [High-level design](docs/design/hld.md) | Architecture, components, data flows, technology stack, deployment, milestones |
| [Low-level design](docs/design/lld.md) | Code structure, schemas, algorithms, APIs, configuration, tests |
| [End-to-end walkthrough](docs/guides/end-to-end-walkthrough.md) | Plain-language tour of the system with a worked example; start here |
| [Exploratory data analysis](docs/eda/eda-report.md) | Profile of the NSL-KDD and CloudTrail datasets, data quality issues, proposed changes |
| [Architecture decision records](docs/adr/README.md) | One record per significant decision |
| [Glossary](docs/glossary.md) | Identifiers, abbreviations and terms used in the documents |
| [Diagrams](docs/diagrams) | Mermaid sources (`.mmd`) and rendered images (`.png`) |
| [Contributing](CONTRIBUTING.md) | Branching, commits, reviews, and documentation conventions |

## Repository layout

```text
.
├── cmd/               One main package per service (ingest, dlm, router) and the loadgen tool
├── internal/          Shared Go packages (catalog, ingest pipeline, search router, OpenSearch and S3 clients, parsers)
├── migrations/        Numbered SQL migrations for the obs_meta database
├── test/failure/      Failure tests (make test-failure-*)
├── deploy/
│   ├── compose/       compose.yaml and Prometheus configuration
│   └── grafana/       Grafana data sources and dashboards (JSON, provisioned at start)
├── docs/
│   ├── adr/           Architecture decision records
│   ├── design/        Requirements, high-level design, low-level design
│   ├── diagrams/      Mermaid diagram sources and rendered PNGs
│   ├── eda/           Exploratory data analysis report, figures, and result summaries
│   └── guides/        Plain-language walkthroughs
├── eda/               Exploratory data analysis notebooks (Jupyter) and dataset download script
├── data/              Raw datasets, downloaded locally (not committed)
├── scripts/           Demo script (make demo)
├── Dockerfile         One image for all Go services
├── .env.example       Local settings; copied to .env by `make up`
├── CONTRIBUTING.md
├── Makefile           Development tasks (run `make help`)
└── README.md
```

## Running locally

```sh
make up      # build the image and start the stack
make ps      # check container status
make down    # stop the stack (data is kept); `make clean` also deletes it
```

| Service | Default address | Port setting |
| --- | --- | --- |
| router (search API) | <http://localhost:8080> | `ROUTER_PORT` |
| ingest | <http://localhost:8081> | `INGEST_PORT` |
| dlm | <http://localhost:8082> | `DLM_PORT` |
| OpenSearch | <http://localhost:9200> | `OPENSEARCH_PORT` |
| PostgreSQL (`obs_meta`) | `localhost:5440` | `POSTGRES_PORT` |
| SeaweedFS (S3 API) | <http://localhost:8333> | `SEAWEEDFS_PORT` |
| Prometheus | <http://localhost:9090> | `PROMETHEUS_PORT` |
| Grafana | <http://localhost:3000> (view without login; admin / admin to edit) | `GRAFANA_PORT` |

Each Go service exposes `/healthz`, `/readyz` and `/metrics`. The stack needs
about 6 GB of free Docker memory.

### Settings

`make up` copies `.env.example` to `.env` the first time. Edit `.env` to
change a host port that is already in use, to give OpenSearch less memory on
a smaller machine (`OPENSEARCH_HEAP`, `OPENSEARCH_MEMORY`), or to change a
service setting such as `ROUTER_COLD_TIMEOUT`. Every optional setting is
listed in `.env.example` with its default; run `make up` again to apply a
change. The settings are described in
[section 7.1 of the low-level design](docs/design/lld.md#71-environment-variables).

### Loading data

`make load` replays the NSL-KDD files in `data/raw/nslkdd` (see
[Common tasks](#common-tasks) to download them) as timestamped log files and
writes them to the ingest inbox. The ingest service picks them up within a few
seconds, copies each file to the Bronze area in SeaweedFS and indexes the
events into a daily `logs-YYYY.MM.DD` index.

```sh
make load args="-target-gb 0.05"   # about 270k events; default is 1 GB
curl -s 'localhost:9200/logs-*/_count'
```

Processed files move to `done/` or `failed/` inside the `inbox` volume. Per-file
results are in the `ingest_batches` table and rejected lines in
`rejected_records`. Loading the same file again adds nothing: every event ID is
derived from the file hash and line number.

### Searching

The router reads the partition catalog to find which tier holds each part of
the time range. HOT and WARM partitions are searched with one OpenSearch
query; COLD partitions are searched with one DuckDB query over the Parquet
files listed in their manifests. Both run at the same time, and the results
are merged newest first with duplicates removed. Parameters are described in
[section 6.4.1 of the low-level design](docs/design/lld.md#641-api).

The search text `q` supports `a b` (both words), `a | b` (either), `-a`
(not), `"a b"` (words next to each other) and `ab*` (prefix). A word is a run
of letters, digits and `_`, so `0.00` means the words `0` and `00` side by
side. Every tier applies the same rules
([decision record](docs/adr/0013-shared-search-text-parser.md)).

```sh
curl -s 'localhost:8080/api/v1/search?from=2026-11-26T00:00:00Z&to=2026-11-27T00:00:00Z&q=ipsweep&severity=WARN&limit=5'
```

The response lists each tier with its time, hit count and status. If one
tier fails or times out (`ROUTER_COLD_TIMEOUT`, default 20 s) the other
tiers' results are still returned and the search is `PARTIAL`. A search that
covers more than `ROUTER_MAX_COLD_WINDOW` (default 7 days) of COLD data is
rejected, because COLD searches scan files rather than an index.

Each search is recorded in the `query_audit` table. Invalid requests return
`400` and a failed search returns `503`, both as problem-details JSON
([RFC 9457](https://www.rfc-editor.org/rfc/rfc9457)).

### Data lifecycle

Every 30 seconds dlm checks the active policy in `tier_policies`. A partition
is sealed (made read-only and counted) once its day has ended and it has had
no writes for `late_window`. A sealed partition older than `hot_after` gets a
`HOT_TO_WARM` job in `migration_jobs`, which force-merges the index, checks the
document count and then switches the catalog to WARM. Job progress is saved
after every step, so a restarted dlm carries on where it stopped.

A WARM partition older than `warm_after` gets a `WARM_TO_COLD` job. dlm reads
the index through a point in time, writes the events to Zstandard-compressed Parquet files
with DuckDB, uploads them to `cold/` in SeaweedFS and uploads the
`_manifest.json` last. It then reads the files back and compares the row count
and fingerprint with the source before pointing the catalog at the manifest.
The WARM index is deleted after `purge_grace`, so searches already running
against it can finish. If any check fails, the attempt's files are deleted and
the job is retried later.

A partition older than `retain_for` gets a `DELETE` job instead. It is marked
`DELETED` in the catalog, so searches stop reading it at once, and its index
or COLD files are removed after `purge_grace`.

### Dashboards

Grafana opens on the system health dashboard. The dashboards are kept as code
in `deploy/grafana/dashboards` and loaded when Grafana starts. Together they
cover the [reports in the requirements](docs/design/requirements.md#5-reports).

| Dashboard | Shows |
| --- | --- |
| [System health](http://localhost:3000/d/obs-system-health) | Services up, ingest and reject rate, CPU and memory per service, search time, ingest batches, rejected records, migration jobs |
| [Storage and search](http://localhost:3000/d/obs-storage-search) | Partitions, size and monthly cost per tier, savings against keeping everything in HOT, median and 95th-percentile search time by tier, partial results |
| [Security events](http://localhost:3000/d/obs-security-events) | Events per hour by category, error events by source, totals by source and category, anomaly-flagged hours |

The security dashboard reads hourly counts that dlm writes to
`obs_gold.fact_events_hourly` while partitions are still in OpenSearch
([decision record](docs/adr/0014-hourly-rollups-in-dlm.md)). An hour is flagged as
an anomaly when its count is at least three standard deviations from the
previous 24 hours of the same source and category, so flags appear only once
there are at least 13 hours of data.

Costs use the prices in the `tier_prices` table: approximate Google Cloud list
prices for us-central1 (SSD persistent disk for HOT, standard persistent disk
for WARM, Cloud Storage Nearline for COLD).

### Demo

```sh
make clean up demo
```

The demo switches to the `demo` policy (hourly partitions, WARM after 1 hour,
COLD after 3 hours), loads a 6-hour backfill and prints the number of
partitions in each tier until all three hold data. In rehearsal this took
about 3 minutes after loading, and about 4 minutes from `make clean`. It then
runs one search per tier and prints the dashboard links. It needs an empty
catalog, so it refuses to run on a stack that already has data.

### Failure tests

These check that searches keep working, and no event is lost, while data
moves between tiers. Each one
needs at least one finished day of data:

```sh
make load args="-target-gb 0.2 -backfill 72h"
make test-failure-dlm-crash        # kill dlm in the middle of a WARM to COLD copy
make test-failure-lost-cold-file   # lose a Parquet file before verification
```

The test searches for a known event of the partition every second, checks
counts and fingerprints at the end, prints PASS or FAIL for each check, and
restores the policy it changed. The lost-file test takes about five minutes because of the
retry backoff and the purge grace period.

```sh
docker compose --env-file .env -f deploy/compose/compose.yaml exec postgres \
  psql -U obs -d obs_meta -c 'select job_id, partition_id, job_type, status, attempt, error_msg from migration_jobs'
```

## Prerequisites

| Tool | Version | Used for |
| --- | --- | --- |
| Node.js | 20 or later | Rendering diagrams and linting Markdown (through `npx`) |
| Python | 3.11 or later | Exploratory data analysis only |
| Go | 1.27 or later | Services |
| Docker with Compose v2 | Current | Local stack |

## Common tasks

```sh
make help            # list available targets
make test            # go vet and unit tests
make docs-lint       # lint all Markdown files
make eda-setup eda-data eda   # set up, download data, run the exploratory data analysis
```

## Team

| Name | Role |
| --- | --- |
| Gajendra Tiwari | Contributor |
| Thiru Narayanasamy | Contributor |
| Vipul Shukla | Contributor |
