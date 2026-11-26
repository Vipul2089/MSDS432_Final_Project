# Contributing

This guide describes how Group 6 works in this repository.

## Workflow

1. Create a branch from `main` named `<type>/<short-description>`, for example
   `feat/dlm-resumable-jobs` or `docs/adr-0013-retention`.
2. Keep each pull request focused on one change. Link it to the requirement,
   ADR, or milestone it serves, for example `Implements FR-03 (M3)`.
3. At least one other team member must approve the pull request before it is
   merged.
4. Use squash merge. The pull request title becomes the commit message.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):

```text
<type>(<scope>): <summary in imperative mood, no trailing period>

<optional body: what changed and why>
```

| Type | Use for |
| --- | --- |
| `feat` | A new feature |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `test` | Adding or changing tests |
| `refactor` | A code change that neither fixes a bug nor adds a feature |
| `perf` | A performance improvement |
| `build` / `ci` | Build system or continuous integration changes |
| `chore` | Maintenance tasks |

Scopes match service or area names: `ingest`, `dlm`, `router`, `loadgen`,
`catalog`, `deploy`, `docs`.

## Documentation conventions

- Documentation lives in `docs/` and is reviewed like code.
- Significant or hard-to-reverse decisions need an architecture decision record
  (ADR). Follow the process in
  [`docs/adr/README.md`](docs/adr/README.md).
- Give requirements, risks, tests and similar items an identifier from the
  [glossary](docs/glossary.md#2-item-identifiers), so they can be traced from
  requirement to design, code and test. Use identifiers in the requirements,
  design documents and decision records, not in the README, guides or
  dashboards.
- Write in plain, active, present-tense English. Use sentence-case headings.
  Define each acronym the first time you use it, and add new ones to the
  glossary.
- Use RFC 2119 keywords (MUST, SHOULD, MAY) only in requirements and
  invariants.
- Keep diagrams as Mermaid sources in `docs/diagrams/`. Run
  `make docs-diagrams` and commit the `.mmd` file and its rendered `.png`
  together.
- Run `make docs-lint` before opening a pull request.
- Baselined documents follow the change-control rules in
  [`docs/README.md`](docs/README.md#change-control).

## Code conventions (from M0)

- Format with `gofmt`. Code must pass `go vet` and `staticcheck`.
- Return errors with context (`fmt.Errorf("seal partition: %w", err)`) instead of
  panicking.
- Pass a `context.Context` as the first argument to every I/O function.
- Log with `log/slog` in JSON format. Never log secrets.
- Write table-driven unit tests for pure functions, and integration tests with
  Testcontainers.
- Never commit secrets. Local secrets go in `.env`, which is listed in
  `.gitignore`.
