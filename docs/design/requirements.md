# Observability Fabric: requirements specification

| Field | Value |
| --- | --- |
| Document ID | SRS-001 |
| Version | 1.2 |
| Status | Baselined |
| Authors | Group 6: Gajendra Tiwari, Thiru Narayanasamy, Vipul Shukla |
| Approved by | Gajendra Tiwari |
| Date | 2026-11-09 |
| Related | [HLD-001](hld.md), [LLD-001](lld.md), [ADRs](../adr/README.md) |

## Table of contents

- [1. Introduction](#1-introduction)
- [2. Goals and non-goals](#2-goals-and-non-goals)
- [3. Functional requirements](#3-functional-requirements)
- [4. Non-functional requirements](#4-non-functional-requirements)
- [5. Reports](#5-reports)
- [6. Traceability to the brief's sample questions](#6-traceability-to-the-briefs-sample-questions)
- [7. Constraints and assumptions](#7-constraints-and-assumptions)
- [8. Revision history](#8-revision-history)

The key words MUST, SHOULD, and MAY are used as described in
[RFC 2119](https://www.rfc-editor.org/rfc/rfc2119). MUST marks a mandatory
requirement; SHOULD marks a lower-priority or stretch requirement.

## 1. Introduction

### 1.1 Purpose

This document defines what Observability Fabric must do and how well it must
do it. Requirements here are the baseline for the
[high-level design](hld.md), the [low-level design](lld.md), and acceptance
testing.

### 1.2 Scope

Observability Fabric ingests security and audit logs, makes them searchable
through one API, and moves them automatically from fast, expensive storage to
cheap storage as they age, without breaking search. A dashboard shows system
health and storage cost in real time.

### 1.3 Sources

| Source | Content used |
| --- | --- |
| Project 6 brief, "Predictive IT Operations (Observability Fabric)" | Business impact, the Go Data Lifecycle Management (DLM) challenge, sample questions SQ-1 to SQ-5, data sources |
| Final Project Phase 1 assignment | Go mandate, Docker and Kubernetes recommendations, deliverables |
| Final Project Phase 2 assignment | Data integration, data quality, and reporting questions |

### 1.4 Definitions and abbreviations

Identifiers such as `FR-` (functional requirement), `NFR-` (non-functional
requirement), `RPT-` (report) and `SQ-` (sample question), and all
abbreviations used here, are defined in the [glossary](../glossary.md).

## 2. Goals and non-goals

### 2.1 Goals

- G-1: Search all ingested logs, whatever tier they are in, through one API.
- G-2: Move data between tiers automatically, with no search failures and no
  data loss.
- G-3: Show system health and storage cost in real time.

### 2.2 Non-goals

- Machine-learning prediction. A statistical anomaly flag (FR-07) is the only
  predictive feature.
- User accounts, access control, and multi-tenancy.
- Hybrid on-premises deployment.
- High availability for PostgreSQL and OpenSearch.
- Production-grade security hardening beyond the measures in
  [HLD §8.3](hld.md#83-security).

## 3. Functional requirements

| ID | Requirement | Acceptance criteria | Source |
| --- | --- | --- | --- |
| FR-01 | A Go ingest service MUST load NSL-KDD (CSV) and AWS CloudTrail (JSON) logs into one unified event schema, and MUST store every rejected record with its reason. | About 1 GB of generated data is loaded. Every rejected record is stored with a reason. Ingesting the same file twice does not change the document count. | Brief, Phase 1 |
| FR-02 | The system MUST provide one search API that filters by free text, time range, source, and severity, and returns matching events from every tier. | One request whose time range covers HOT and COLD data returns events from both, each labelled with its tier. | Brief |
| FR-03 | A Go DLM service MUST move data from HOT to WARM to COLD, and delete it after retention, according to configurable age rules. | With the demo policy, a 6-hour backfill has partitions in all three tiers within 15 minutes, without manual action. | Brief |
| FR-04 | Searches MUST NOT fail or miss events because of a migration, and a migration MUST NOT lose or duplicate events. An interrupted migration MUST resume automatically. | Failure tests FT-01 to FT-03 pass while a search runs every second. | Brief, SQ-3 |
| FR-05 | The system MUST provide a real-time dashboard showing system health and business metrics. | Reports RPT-01 to RPT-06 are live in Grafana. | Brief, Phase 1 |
| FR-06 | One command MUST start the full local stack. Lifecycle moves and aggregation MUST run on schedules without manual steps. CI MUST build and test every pull request. | `make up` starts the stack; partitions move with no operator action; CI runs on every pull request. | Phase 1 |
| FR-07 | The system SHOULD flag hours whose event count differs strongly from its recent baseline. | Flagged hours appear in RPT-03. | Project title |

## 4. Non-functional requirements

| ID | Category | Requirement | Target |
| --- | --- | --- | --- |
| NFR-01 | Constraint | Ingestion, concurrency, and orchestration logic MUST be written in Go. | All services in Go; Python only for exploratory data analysis (EDA) |
| NFR-02 | Portability | All services MUST run in Docker from one image. The system SHOULD deploy to GKE with liveness and readiness probes. | Docker: required. GKE: stretch goal |
| NFR-03 | Scalability | The system MUST be validated at about 10 GB locally, and SHOULD be validated at 100 GB on GKE. | 10 GB: required. 100 GB: stretch goal |
| NFR-04 | Performance | HOT search and data freshness MUST meet their targets. COLD search latency MUST be measured and reported. | HOT search p95 < 2 s over a 1-day window; new data searchable within 60 s (p95); COLD: measured, no target |
| NFR-05 | Observability | Every service MUST expose health and Prometheus metrics endpoints and write structured logs. Container CPU and memory MUST be reported under defined load profiles. | `/healthz`, `/readyz`, `/metrics`; JSON logs; profiles in [LLD §10.4](lld.md#104-resource-profiles) |

The numeric targets in NFR-04 are the team's proposals; the brief gives no
numbers. Measurements at M5 confirm or revise them.

## 5. Reports

| ID | Report | Content | Refresh | Data source |
| --- | --- | --- | --- | --- |
| RPT-01 | System health | Service up or down, ingest rate, reject rate, container CPU and memory, failed DLM jobs | 10 s | Prometheus |
| RPT-02 | Data volume and quality | Events per hour by source, rejected records by reason, batch status | 1 min | `obs_meta.ingest_batches`, `obs_meta.rejected_records`, `obs_gold.fact_events_hourly` |
| RPT-03 | Security events and anomalies | Events by source and category, error counts, anomaly-flagged hours | 1 min | `obs_gold.fact_events_hourly` |
| RPT-04 | Storage tier and cost | Partitions, GB, and estimated monthly cost per tier; savings against keeping everything in HOT | 5 min | `obs_meta.data_partitions`, `obs_meta.tier_prices` |
| RPT-05 | Search performance | p50 and p95 search latency by tier; partial results | 1 min | `obs_meta.query_audit` |
| RPT-06 | Lifecycle audit | Migration jobs by type and status, durations, failures, retries | 1 min | `obs_meta.migration_jobs` |

RPT-04 provides the business metrics required by Phase 1. RPT-01 and RPT-05
provide the performance metrics.

## 6. Traceability to the brief's sample questions

| Sample question | Requirements | Evidence |
| --- | --- | --- |
| SQ-1: How index sharding keeps search fast past 100 GB | NFR-03, NFR-04 | [LLD §4.3](lld.md#43-shard-sizing), [ADR-0001](../adr/0001-partition-by-time-bucket.md) |
| SQ-2: CPU and memory impact of the service in Docker | NFR-05 | RPT-01, [LLD §10.4](lld.md#104-resource-profiles) |
| SQ-3: Integrity and availability during tier moves | FR-04 | [HLD §5](hld.md#5-design-invariants), FT-01 to FT-03 |
| SQ-4: Full-text search performance, hot versus cold | FR-02, NFR-04 | RPT-05, [LLD §10.3](lld.md#103-hot-versus-cold-benchmark) |
| SQ-5 (bonus): Kubernetes liveness probe | NFR-02 | [LLD §8](lld.md#8-health-endpoints-and-probes), FT-04 |

## 7. Constraints and assumptions

- NSL-KDD has no timestamps or IP addresses. The load generator adds them and
  marks the events as synthetic.
- CloudTrail timestamps are shifted to recent dates. Shifted events are also
  marked as synthetic.
- Local development machines have at least 16 GB of RAM.
- The users are the project team, and query concurrency is low.
- Implementation must fit the course timeline. Work beyond the MVP is labelled
  as a stretch goal.

## 8. Revision history

| Version | Date | Author | Change |
| --- | --- | --- | --- |
| 1.0 | 2026-11-09 | Group 6 | Baseline: data sources NSL-KDD and CloudTrail; HOT, WARM, and COLD tiers; anomaly flag only; GKE as a stretch goal |
| 1.1 | 2026-11-27 | Group 6 | Added §1.4, which points to the glossary for identifiers and abbreviations |
| 1.2 | 2026-11-27 | Group 6 | RPT-04 data source: cost is computed from `data_partitions` and `tier_prices` (ADR-0014) |
