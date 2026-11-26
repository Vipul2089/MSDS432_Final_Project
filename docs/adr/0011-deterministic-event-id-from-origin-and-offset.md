# ADR-0011: Derive event_id from origin and offset

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: INV-5, FT-03, [LLD §3.3](../design/lld.md#33-event-id)

## Context

`event_id` is the OpenSearch `_id`, so it must be the same every time a record
is ingested (INV-5). Phase 2 defined it as a SHA-256 of key fields. NSL-KDD
contains rows that are identical but represent separate connections. A content
hash would merge those rows into one document and silently lose events.

## Decision

Derive `event_id` from where the record came from, not from its content:

```text
digest   = SHA-256(source + 0x1F + origin_id + 0x1F + decimal(offset))
event_id = lowercase hex of digest[0:16]
```

- `origin_id` is the SHA-256 of the input file.
- `offset` is the record's zero-based position in that file.

## Consequences

### Positive

- Re-ingesting the same file produces the same IDs.
- Identical source rows remain separate events.

### Negative

- The same event delivered in two different files would get two IDs. In this
  project every event comes from exactly one `loadgen` file, so this does not
  occur.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| SHA-256 of the record content | Merges legitimate duplicate records |
| Random UUID | Re-ingesting creates duplicates |
