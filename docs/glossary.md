# Observability Fabric: glossary

| Field | Value |
| --- | --- |
| Document ID | GLOSSARY-001 |
| Version | 1.1 |
| Status | Baselined |
| Authors | Group 6: Gajendra Tiwari, Thiru Narayanasamy, Vipul Shukla |
| Date | 2026-11-27 |
| Related | [SRS-001](design/requirements.md), [HLD-001](design/hld.md), [LLD-001](design/lld.md), [ADRs](adr/README.md) |

This glossary defines the identifiers, abbreviations and terms used across
the project documents. The requirements, design documents and architecture
decision records use these short forms and link here; the README and the
walkthrough use full words instead.

## Table of contents

- [1. Document identifiers](#1-document-identifiers)
- [2. Item identifiers](#2-item-identifiers)
- [3. Abbreviations](#3-abbreviations)
- [4. Terms](#4-terms)
- [5. Revision history](#5-revision-history)

## 1. Document identifiers

| Identifier | Document |
| --- | --- |
| SRS-001 | [Software requirements specification](design/requirements.md) |
| HLD-001 | [High-level design](design/hld.md) |
| LLD-001 | [Low-level design](design/lld.md) |
| GUIDE-001 | [End-to-end walkthrough](guides/end-to-end-walkthrough.md) |
| EDA-001 | [Exploratory data analysis report](eda/eda-report.md) |
| GLOSSARY-001 | This glossary |
| ADR-NNNN | [Architecture decision record](adr/README.md) number NNNN |

## 2. Item identifiers

Requirements, risks, tests and other items carry an identifier so that they
can be traced from requirement to design, code and test.

| Prefix | Full form | Defined in |
| --- | --- | --- |
| `G-` | Goal | SRS-001 §2 |
| `FR-` | Functional requirement | SRS-001 §3 |
| `NFR-` | Non-functional requirement | SRS-001 §4 |
| `RPT-` | Report: a dashboard view the system must provide | SRS-001 §5 |
| `SQ-` | Sample question from the project brief | SRS-001 §6 |
| `INV-` | Design invariant: a rule that must always hold | HLD-001 §5 |
| `RSK-` | Risk | HLD-001 §11 |
| `M0` to `M6` | Milestone | HLD-001 §12 |
| `FT-` | Failure test | LLD-001 §10.2 |
| `BQ-` | Benchmark query | LLD-001 §10.3 |
| `SP-` | Validation spike: a small experiment that tests a risky assumption | LLD-001 §11 |
| `DQ-` | Data quality issue | EDA-001 |
| `EDA-F` | Finding of the exploratory data analysis | EDA-001 |
| `PC-` | Proposed change to the design, from the exploratory data analysis | EDA-001 |

The words MUST, SHOULD and MAY in requirements are used as defined in
[RFC 2119](https://www.rfc-editor.org/rfc/rfc2119).

## 3. Abbreviations

| Abbreviation | Full form |
| --- | --- |
| API | Application programming interface |
| ASCII | American Standard Code for Information Interchange |
| AWS | Amazon Web Services |
| CI | Continuous integration |
| CPU | Central processing unit |
| CSV | Comma-separated values |
| DLM | Data lifecycle management; also the `dlm` service that performs it |
| DoS | Denial of service (an NSL-KDD attack category) |
| DSN | Data source name: the PostgreSQL connection string |
| EPS | Events per second |
| FK | Foreign key |
| FTS | Full-text search |
| GB, MB, KB | Gigabyte, megabyte, kilobyte |
| GCP | Google Cloud Platform |
| GCS | Google Cloud Storage |
| GKE | Google Kubernetes Engine |
| HDFS | Hadoop Distributed File System (a candidate log source that was not used) |
| HMAC | Hash-based message authentication code; GCS keys of this type allow S3-compatible access |
| HTTP | Hypertext Transfer Protocol |
| IAM | Identity and Access Management (an AWS service) |
| ICMP, TCP, UDP | Internet Control Message Protocol, Transmission Control Protocol, User Datagram Protocol |
| ID | Identifier |
| IP | Internet Protocol (address) |
| ISM | Index State Management, the OpenSearch lifecycle plugin |
| JSON | JavaScript Object Notation |
| MVP | Minimum viable product: the required scope, without stretch goals |
| NDJSON | Newline-delimited JSON, the format of OpenSearch bulk requests |
| NSL-KDD | Network intrusion dataset from the University of New Brunswick, a refined version of KDD Cup 1999 |
| p50, p95 | 50th and 95th percentile |
| PIT | Point in time: an OpenSearch view of an index that stays fixed while it is read page by page |
| PK | Primary key |
| R2L, U2R | Remote to local, user to root (NSL-KDD attack categories) |
| RFC | Request for Comments, an internet standard document |
| S3 | Amazon Simple Storage Service; here, the S3-compatible API that SeaweedFS and GCS also offer |
| SDK | Software development kit |
| SHA-256 | Secure Hash Algorithm with a 256-bit digest |
| SQL | Structured Query Language |
| SSD | Solid-state drive |
| STS | Security Token Service (an AWS service) |
| SYN | Synchronise: the first packet of a TCP connection; a SYN flood is a DoS attack |
| TLS | Transport Layer Security |
| URI | Uniform resource identifier |
| UTC | Coordinated Universal Time |
| UUID | Universally unique identifier |
| VPC | Virtual private cloud |
| ZSTD | Zstandard, the compression used for Parquet files |

## 4. Terms

| Term | Meaning |
| --- | --- |
| Backfill | Loading events whose timestamps lie in the past, for example the last 72 hours |
| Bronze | Unchanged copies of the input files in the object store, under `bronze/` |
| Bucket | The time range one partition covers: one day by default, one hour in the demo policy |
| Catalog | The `data_partitions` table, the single record of where each partition can be read |
| COLD | Tier for the oldest data: Parquet files in the object store, searched with DuckDB |
| Fingerprint | Sum of the event hashes of a partition modulo 2^64, used to check a copy |
| Grace period (`purge_grace`) | Time an old copy is kept after the catalog points to the new one, so running searches can finish |
| HOT | Tier for recent data: a writable OpenSearch index |
| Late window (`late_window`) | Time with no new writes after which a finished bucket is sealed |
| Manifest | `_manifest.json`, written last, that lists the Parquet files of a COLD partition |
| Migration job | A row in `migration_jobs` that moves or deletes one partition step by step |
| Partition | All events of one bucket, stored together and moved together |
| Policy | A row in `tier_policies` that sets when partitions are sealed, moved and deleted |
| Rollup | Hourly event counts per source and category in `obs_gold.fact_events_hourly` |
| Seal | Make a partition read-only and record its exact event count |
| Tier | Where a partition is stored: HOT, WARM or COLD |
| WARM | Tier for older data: a read-only, force-merged OpenSearch index |

## 5. Revision history

| Version | Date | Author | Change |
| --- | --- | --- | --- |
| 1.0 | 2026-11-27 | Group 6 | First version |
| 1.1 | 2026-11-27 | Group 6 | Removed `OQ-`, because HLD-001 no longer has open questions |
