package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrVersionConflict means a partition changed while a job was moving it.
var ErrVersionConflict = errors.New("partition version changed")

// SealCandidates returns OPEN partitions whose bucket has ended and that have
// had no writes for the policy's late_window (LLD-001 §6.3.1).
func (c *Catalog) SealCandidates(ctx context.Context) ([]Partition, error) {
	rows, err := c.db.Query(ctx,
		`SELECT p.partition_id, p.bucket_start, p.bucket_end, p.tier, p.state, p.location
		   FROM data_partitions p JOIN tier_policies t ON t.is_active
		  WHERE p.state = 'OPEN'
		    AND now() >= p.bucket_end + t.late_window
		    AND now() >= coalesce(p.last_write_at, p.created_at) + t.late_window
		  ORDER BY p.bucket_start`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Partition, error) {
		var p Partition
		err := row.Scan(&p.ID, &p.Start, &p.End, &p.Tier, &p.State, &p.Location)
		return p, err
	})
}

// OpenPartitions returns the partitions that are still receiving data.
func (c *Catalog) OpenPartitions(ctx context.Context) ([]Partition, error) {
	rows, err := c.db.Query(ctx,
		`SELECT partition_id, bucket_start, bucket_end, tier, state, location
		   FROM data_partitions WHERE state = 'OPEN' ORDER BY bucket_start`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Partition, error) {
		var p Partition
		err := row.Scan(&p.ID, &p.Start, &p.End, &p.Tier, &p.State, &p.Location)
		return p, err
	})
}

// UpdateOpenStats records the current size of an open partition for the
// dashboards. Sealing overwrites both values with exact figures.
func (c *Catalog) UpdateOpenStats(ctx context.Context, id, docs, bytes int64) error {
	_, err := c.db.Exec(ctx,
		`UPDATE data_partitions SET doc_count = $2, size_bytes = $3
		  WHERE partition_id = $1 AND state = 'OPEN'`, id, docs, bytes)
	return err
}

func (c *Catalog) SealPartition(ctx context.Context, id, docs, bytes int64) error {
	_, err := c.db.Exec(ctx,
		`UPDATE data_partitions
		    SET state = 'SEALED', doc_count = $2, size_bytes = $3, sealed_at = now(), updated_at = now()
		  WHERE partition_id = $1 AND state = 'OPEN'`, id, docs, bytes)
	return err
}

// PlanJobs creates the jobs that the active policy calls for (LLD-001
// §6.3.1) and returns how many were created. Each partition gets at most one
// rule, and deletion wins over a move. A partition moves to COLD only after
// its final hourly rollup, because COLD has no index to aggregate. The unique index on active jobs makes
// this safe to repeat. A partition whose last job of that type FAILED is left
// alone until someone looks at it.
func (c *Catalog) PlanJobs(ctx context.Context) (int64, error) {
	tag, err := c.db.Exec(ctx,
		`INSERT INTO migration_jobs (partition_id, job_type)
		 SELECT d.partition_id, d.job_type
		   FROM (SELECT p.partition_id,
		                CASE WHEN now() - p.bucket_end >= t.retain_for THEN 'DELETE'
		                     WHEN p.state = 'SEALED' AND p.tier = 'HOT' AND now() - p.bucket_end >= t.hot_after THEN 'HOT_TO_WARM'
		                     WHEN p.state = 'SEALED' AND p.tier = 'WARM' AND now() - p.bucket_end >= t.warm_after
		                          AND p.gold_final_at IS NOT NULL THEN 'WARM_TO_COLD'
		                END AS job_type
		           FROM data_partitions p JOIN tier_policies t ON t.is_active
		          WHERE p.state <> 'DELETED') d
		  WHERE d.job_type IS NOT NULL
		    AND NOT EXISTS (SELECT 1 FROM migration_jobs j
		                     WHERE j.partition_id = d.partition_id
		                       AND j.job_type = d.job_type AND j.status = 'FAILED')
		 ON CONFLICT DO NOTHING`)
	return tag.RowsAffected(), err
}

type Job struct {
	ID             int64
	PartitionID    int64
	Type           string
	Status         string
	Attempt        int
	MaxAttempts    int
	StagingURI     string
	SrcCount       int64
	SrcFingerprint string // decimal, empty until the copy has run

	Start, End time.Time // the partition's bucket
	Tier       string    // the partition's current tier
	Location   string    // the partition's current location
	DocCount   int64     // recorded when the partition was sealed
	Version    int
}

// NextJob returns the oldest unfinished job that is due to run.
func (c *Catalog) NextJob(ctx context.Context) (Job, bool, error) {
	var j Job
	err := c.db.QueryRow(ctx,
		`SELECT j.job_id, j.partition_id, j.job_type, j.status, j.attempt, j.max_attempts,
		        coalesce(j.staging_uri, ''), coalesce(j.src_count, 0), coalesce(j.src_fingerprint::text, ''),
		        p.bucket_start, p.bucket_end, p.tier, p.location, coalesce(p.doc_count, 0), p.version
		   FROM migration_jobs j JOIN data_partitions p USING (partition_id)
		  WHERE j.status NOT IN ('DONE', 'FAILED') AND j.next_run_at <= now()
		  ORDER BY j.job_id
		  LIMIT 1`,
	).Scan(&j.ID, &j.PartitionID, &j.Type, &j.Status, &j.Attempt, &j.MaxAttempts,
		&j.StagingURI, &j.SrcCount, &j.SrcFingerprint,
		&j.Start, &j.End, &j.Tier, &j.Location, &j.DocCount, &j.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// StartAttempt counts a new attempt, records where it writes (if anywhere)
// and moves the job to status.
func (c *Catalog) StartAttempt(ctx context.Context, id int64, status, stagingURI string) error {
	_, err := c.db.Exec(ctx,
		`UPDATE migration_jobs
		    SET attempt = attempt + 1, status = $2, staging_uri = nullif($3, ''),
		        started_at = coalesce(started_at, now())
		  WHERE job_id = $1`, id, status, stagingURI)
	return err
}

// MoveJob sets a job's status and the earliest time it runs again. A
// non-empty errMsg replaces the saved error.
func (c *Catalog) MoveJob(ctx context.Context, id int64, status, errMsg string, runAt time.Time) error {
	var finished *time.Time
	if status == "FAILED" {
		now := time.Now()
		finished = &now
	}
	_, err := c.db.Exec(ctx,
		`UPDATE migration_jobs
		    SET status = $2, error_msg = coalesce(nullif($3, ''), error_msg), next_run_at = $4, finished_at = $5
		  WHERE job_id = $1`, id, status, errMsg, runAt, finished)
	return err
}

// SetJobSource records what was read from the source tier. fingerprint is a
// decimal string, or empty if the job does not compute one.
func (c *Catalog) SetJobSource(ctx context.Context, id, count int64, fingerprint string) error {
	_, err := c.db.Exec(ctx,
		`UPDATE migration_jobs SET src_count = $2, src_fingerprint = nullif($3, '')::numeric WHERE job_id = $1`,
		id, count, fingerprint)
	return err
}

// SetJobTarget records what was found in the target tier.
func (c *Catalog) SetJobTarget(ctx context.Context, id, count int64, fingerprint string) error {
	_, err := c.db.Exec(ctx,
		`UPDATE migration_jobs SET dst_count = $2, dst_fingerprint = nullif($3, '')::numeric WHERE job_id = $1`,
		id, count, fingerprint)
	return err
}

// SwitchTier points the partition at its new tier and finishes the job in
// one transaction. It fails with ErrVersionConflict if the partition is no
// longer at the version and tier the job started from.
func (c *Catalog) SwitchTier(ctx context.Context, j Job, from, to string, sizeBytes int64) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE data_partitions
		    SET tier = $4, size_bytes = $5, version = version + 1, updated_at = now()
		  WHERE partition_id = $1 AND version = $2 AND tier = $3`,
		j.PartitionID, j.Version, from, to, sizeBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	if _, err := tx.Exec(ctx,
		`UPDATE migration_jobs SET status = 'DONE', finished_at = now() WHERE job_id = $1`, j.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SwitchToCold points the partition at its COLD manifest and schedules the
// WARM index for deletion after purge_grace, in one transaction.
func (c *Catalog) SwitchToCold(ctx context.Context, j Job, manifestURI, fingerprint string, sizeBytes int64) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE data_partitions
		    SET tier = 'COLD', location = $3, fingerprint = $4::numeric, size_bytes = $5,
		        version = version + 1, updated_at = now()
		  WHERE partition_id = $1 AND version = $2 AND tier = 'WARM'`,
		j.PartitionID, j.Version, manifestURI, fingerprint, sizeBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return schedulePurge(ctx, tx, j.ID)
}

// MarkDeleted takes the partition out of search and schedules its data for
// deletion after purge_grace, in one transaction.
func (c *Catalog) MarkDeleted(ctx context.Context, j Job) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE data_partitions SET state = 'DELETED', version = version + 1, updated_at = now()
		  WHERE partition_id = $1 AND version = $2`, j.PartitionID, j.Version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return schedulePurge(ctx, tx, j.ID)
}

// schedulePurge moves the job to PURGE_PENDING and commits tx. Searches that
// planned against the old location can still finish during the grace period.
func schedulePurge(ctx context.Context, tx pgx.Tx, jobID int64) error {
	if _, err := tx.Exec(ctx,
		`UPDATE migration_jobs
		    SET status = 'PURGE_PENDING',
		        purge_after = now() + (SELECT purge_grace FROM tier_policies WHERE is_active),
		        next_run_at = now() + (SELECT purge_grace FROM tier_policies WHERE is_active)
		  WHERE job_id = $1`, jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishJob marks a job DONE.
func (c *Catalog) FinishJob(ctx context.Context, id int64) error {
	_, err := c.db.Exec(ctx,
		`UPDATE migration_jobs SET status = 'DONE', finished_at = now() WHERE job_id = $1`, id)
	return err
}
