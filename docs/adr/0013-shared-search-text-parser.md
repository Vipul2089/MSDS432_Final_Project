# ADR-0013: Parse search text once for all tiers

- Status: Accepted
- Date: 2026-11-26
- Deciders: Group 6
- Supersedes: [ADR-0010](0010-cold-text-search-by-regex-scan.md)
- Related: FR-02, [LLD §6.4.1](../design/lld.md#641-api),
  [LLD §6.4.2](../design/lld.md#642-planning-and-execution)

## Context

ADR-0010 sent the search text to OpenSearch as `simple_query_string` and
split it into whole-word regex patterns for COLD. Two differences between the
tiers followed:

- The operators `-`, `|` and quotes worked on HOT and WARM but were matched
  as plain words on COLD.
- The standard analyzer does not split on a comma between digits, so a
  record such as `0,0,0.00,0.00,1.00` was indexed as one token. Searches for
  numbers found nothing on HOT but did match on COLD.

The same search could therefore return different results depending on the
age of the data.

## Decision

The router parses the search text once and builds both the OpenSearch query
and the COLD SQL from the result. Both tiers use one definition of a word.

- **Word.** A run of ASCII letters, digits and `_`, lowercased. HOT indexes
  `raw_message` with a `pattern` analyzer that splits on `\W+` and
  lowercases, which produces the same words.
- **Syntax.** `a b` (both), `a | b` (either; binds tighter than the space),
  `-a` (not), `"a b"` (adjacent words), `ab*` (prefix of one word). Other
  operator characters, a negated term inside an `|` group, and an unclosed
  quote return `400`.
- **HOT and WARM.** Each term becomes `match_phrase` (or `prefix`) on
  `raw_message`; `|` groups become `bool.should`, and negated terms go in
  `bool.must_not`.
- **COLD.** Each clause becomes one bound-parameter
  `regexp_matches(lower(raw_message), ?)`, negated with `NOT` when needed.
  Words are joined with `\W+` between `\b` anchors, and `|` groups become one
  alternation. Words contain only `[a-z0-9_]`, so they need no escaping.

## Consequences

### Positive

- A search returns the same events whichever tier holds the data. A test
  checks HOT and COLD against an independent implementation of the word rule
  for every operator.
- Numbers and IP address parts in `raw_message` are searchable on HOT.
- Unsupported syntax fails clearly instead of being read differently.

### Negative

- Indices created before this change use the standard analyzer and must be
  rebuilt to match. Analyzers cannot be changed on an existing index.
- No stemming, synonyms or fuzzy matching; the analyzer only splits and
  lowercases.
- COLD still scans the selected row groups, so the limits from ADR-0010
  (`ROUTER_MAX_COLD_WINDOW`) still apply.

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| Keep `simple_query_string` and mimic it in SQL | Its tokenizer (Unicode text segmentation) and operator precedence would have to be reimplemented in regex; differences would remain |
| Reject all operators on every tier | Consistent, but loses exclusion, alternatives and phrases |
| The DuckDB full-text search extension | Needs index tables built and stored, a second copy of the data (as in ADR-0010) |
