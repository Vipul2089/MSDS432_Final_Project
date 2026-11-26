# ADR-0012: Base the liveness probe on an internal watchdog

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: NFR-02, NFR-05, SQ-5, [LLD §8](../design/lld.md#8-health-endpoints-and-probes)

## Context

Kubernetes restarts a pod when its liveness probe fails. If liveness checked
dependencies such as PostgreSQL, one database outage would make Kubernetes
restart every pod in a loop, which makes the outage worse. A process that is
deadlocked or stuck can still accept HTTP connections, so a trivial
`200 OK` handler would not detect it.

## Decision

Each long-running loop (for example the planner, the job runner, and the bulk
flusher) updates an atomic heartbeat timestamp. `/healthz` returns 500 if any
heartbeat is older than 2 min.
Dependency checks belong only in `/readyz`, which removes the pod from load
balancing without restarting it. For demos, a `/debug/hang` endpoint, enabled
only by a flag, blocks a loop to show an automatic restart.

## Consequences

### Positive

- Detects hung processes as well as crashed ones.
- A dependency outage does not cause restart loops.

### Negative

- Every long-running loop must update its heartbeat, including while idle.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| A handler that always returns 200 | Does not detect deadlocks |
| Liveness that checks dependencies | Causes restart loops during dependency outages |
