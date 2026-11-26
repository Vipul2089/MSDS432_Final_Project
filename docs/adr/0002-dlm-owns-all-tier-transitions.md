# ADR-0002: The DLM performs every tier transition

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-03, NFR-01, INV-1, [LLD §6.3](../design/lld.md#63-dlm)

## Context

OpenSearch Index State Management (ISM) can automate HOT to WARM moves. The
course requires the lifecycle logic to be written in Go (NFR-01). ISM cannot
export data to Parquet in GCS. If ISM and the DLM both changed the same index,
the catalog could disagree with the index's actual state, which would break
INV-1.

## Decision

Disable ISM. The Go DLM service performs every transition: sealing,
HOT → WARM, WARM → COLD, and deletion. It records each migration step in
`migration_jobs`.

## Consequences

### Positive

- One component controls tier state, so the catalog always matches reality.
- Every transition is visible in RPT-06 and in DLM metrics.
- Meets the course requirement for Go.

### Negative

- The team must implement steps that ISM already provides, such as
  force-merge and allocation changes.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| ISM for HOT → WARM, DLM for WARM → COLD | Two controllers acting on the same index |
| ISM only | Cannot write Parquet, and does not meet NFR-01 |
