# ADR-0005: Run migrations as resumable jobs with a single worker

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-04, INV-6, INV-7, FT-01, [LLD §6.3.4](../design/lld.md#634-recovery-and-retries)

## Context

A migration can be interrupted at any step by a crash, a restart, or a
deployment. It must resume automatically without losing or duplicating data
(FR-04). With daily partitions there are only a few migrations a day, so
running several migrations in parallel brings no benefit.

## Decision

Run `dlm` as a single active instance that processes one job at a time.

- Jobs are rows in `migration_jobs`. Each status change is committed before
  the next step starts.
- On startup, the worker continues every unfinished job from its saved
  status. Every step can be repeated safely.
- A PostgreSQL session advisory lock ensures that only one `dlm` instance is
  active. A second instance waits.
- A partial unique index on `partition_id`, covering unfinished jobs, allows
  at most one active job per partition.

## Consequences

### Positive

- Simple to build, test, and explain.
- Automatic, measurable recovery after a crash (FT-01).

### Negative

- Migrations run one at a time. This is acceptable at the target volume.
- If the active instance hangs without crashing, migrations stop until the
  watchdog restarts it ([ADR-0012](0012-watchdog-based-liveness-probe.md)).

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| Parallel workers with leases and `FOR UPDATE SKIP LOCKED` | More complex than needed at this volume. Can be added later without schema changes |
| An external job queue | Adds infrastructure for no benefit |
