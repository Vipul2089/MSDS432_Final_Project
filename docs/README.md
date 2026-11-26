# Documentation

| Path | Contents |
| --- | --- |
| [`design/requirements.md`](design/requirements.md) | Requirements specification (SRS-001) |
| [`design/hld.md`](design/hld.md) | High-level design (HLD-001) |
| [`design/lld.md`](design/lld.md) | Low-level design (LLD-001) |
| [`guides/end-to-end-walkthrough.md`](guides/end-to-end-walkthrough.md) | Plain-language walkthrough with a worked example (GUIDE-001) |
| [`eda/eda-report.md`](eda/eda-report.md) | Exploratory data analysis of the input datasets (EDA-001) |
| [`adr/`](adr/README.md) | Architecture decision records and template |
| [`diagrams/`](diagrams) | Mermaid diagram sources and rendered PNGs |

New to the project? Start with the walkthrough. Then read the requirements,
the HLD, and the LLD. ADRs explain why each major decision was made.

## Diagrams

| Source | Shows | Used in |
| --- | --- | --- |
| [`01-architecture.mmd`](diagrams/01-architecture.mmd) | System components and data flow | HLD §2.2 |
| [`02-ingest-pipeline.mmd`](diagrams/02-ingest-pipeline.mmd) | Ingest concurrency and error paths | LLD §6.2 |
| [`03-migration-state-machine.mmd`](diagrams/03-migration-state-machine.mmd) | Migration job statuses | HLD §3.2 |
| [`04-search-during-migration.mmd`](diagrams/04-search-during-migration.mmd) | A search that runs while a partition migrates | HLD §3.3 |
| [`05-end-to-end-flow.mmd`](diagrams/05-end-to-end-flow.mmd) | Detailed end-to-end flow with technologies and reasons | Walkthrough §4 |

To render the diagrams, run `make docs-diagrams` from the repository root.

## Change control

Baselined documents (requirements, HLD, LLD) change only through a pull
request that:

- Increments the document's version.
- Adds a row to its revision history.
- Updates any affected ADR, diagram, or cross-reference in the same pull
  request.
