# ADR-0010: Search COLD text by regex scan

- Status: Superseded by [ADR-0013](0013-shared-search-text-parser.md)
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-02, SQ-4, [LLD §6.4.2](../design/lld.md#642-planning-and-execution)

## Context

OpenSearch matches analysed tokens using an inverted index. COLD Parquet files
have no inverted index. Searching them must be reasonably consistent with
OpenSearch and safe from injection.

## Decision

Split the search text into terms. For each term, the router builds the
pattern `\b` + `regexp.QuoteMeta(lower(term))` + `\b` in Go and passes it as a
bound parameter to `regexp_matches(lower(raw_message), ?)`. All terms must
match, as with OpenSearch's `AND` operator. Exact-value filters use equality,
which bloom filters can speed up. Each result is labelled with the tier it
came from.

## Consequences

### Positive

- Needs no extra index; data is stored only once.
- Matching on word boundaries is close to token matching, and the difference
  can be measured.

### Negative

- Every query scans the selected row groups, so COLD latency grows with the
  time window. The router limits the COLD time span to
  `ROUTER_MAX_COLD_WINDOW`.
- Matching does not use stemming or synonyms.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| The DuckDB full-text search (FTS) extension | Needs index tables to be built and stored, which is a second copy of the data |
| `LIKE '%term%'` | Also matches substrings, such as `timeouts` for `timeout` |
