# ADR-0006: Commit COLD writes by writing the manifest last

- Status: Accepted
- Date: 2026-11-09
- Deciders: Group 6
- Related: FR-04, INV-4, INV-7, FT-01, FT-02, [LLD §4.4](../design/lld.md#44-cold-object-layout)

## Context

Object stores do not provide transactions that cover several objects. If a
worker crashes partway through writing a partition's files, readers could see
an incomplete set of files.

## Decision

Write every file for an attempt under
`…/job={job_id}/attempt={n}/`. After all data files are written, write
`_manifest.json`. It lists each file's key, row count, size, and SHA-256, plus
the partition's total row count, fingerprint, and time range. A COLD partition
exists only when its manifest exists and the catalog points to that manifest.
Rollback deletes the attempt prefix.

## Consequences

### Positive

- A crash at any point leaves only unreferenced objects. The job's next
  attempt or its rollback deletes them.
- Readers never list the object store. They read the file list from the
  manifest.

### Negative

- Each file must be hashed before upload, so files are written to a local
  spool directory first.
- The manifest format must be versioned (`manifest_version`).

## Alternatives considered

| Option | Reason rejected |
| --- | --- |
| Write directly to the final paths | Readers could see a partially written partition |
| Apache Iceberg or Delta Lake | Correct, but too heavy for the course timeline and adds a catalog service |
