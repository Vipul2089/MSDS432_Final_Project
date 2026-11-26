# ADR-0008: Use no message queue in the MVP

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-01, [LLD §6.2](../design/lld.md#62-ingest)

## Context

Message queues such as Kafka, Redpanda, NATS, and Pub/Sub absorb bursts of
traffic and allow events to be replayed. In this project, every input is a
file written by `loadgen` into the ingest inbox. Bronze keeps every raw file,
so it can serve as the replay source.

## Decision

Do not add a message queue to the MVP. Bounded Go channels provide
backpressure. Ingestion sits behind a `Source` interface, so a queue-based
source can be added later without changing the rest of the pipeline.

## Consequences

### Positive

- Fewer components to run, monitor, and pay for.

### Negative

- The system cannot accept events pushed over the network. Adding that would
  need a queue or an HTTP intake endpoint.
- A file that is being processed when `ingest` crashes is processed again from
  the start. Deterministic event IDs make this safe (INV-5).

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| Kafka or Redpanda | Operational cost with no benefit at prototype rates |
| Google Pub/Sub | Available on GKE only, so local and cloud setups would differ |
