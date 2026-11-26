// Package catalog reads and writes the obs_meta tables in PostgreSQL. The
// data_partitions table is the single source of truth for where each time
// bucket of data can be read (INV-1).
package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Catalog struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Catalog {
	return &Catalog{db: db}
}

// Policy is the active lifecycle policy (LLD-001 §7.2).
type Policy struct {
	Name       string
	BucketSize time.Duration
	RetainFor  time.Duration
}

func (c *Catalog) ActivePolicy(ctx context.Context) (Policy, error) {
	var (
		p                 Policy
		bucket, retainFor pgtype.Interval
	)
	err := c.db.QueryRow(ctx,
		`SELECT policy_name, bucket_size, retain_for FROM tier_policies WHERE is_active`,
	).Scan(&p.Name, &bucket, &retainFor)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, errors.New("no active tier policy")
	}
	if err != nil {
		return p, err
	}
	if p.BucketSize, err = duration(bucket); err != nil {
		return p, fmt.Errorf("bucket_size: %w", err)
	}
	if p.RetainFor, err = duration(retainFor); err != nil {
		return p, fmt.Errorf("retain_for: %w", err)
	}
	return p, nil
}

// duration converts a Postgres interval to a time.Duration. Month-based
// intervals have no fixed length, so they are not allowed in policies.
func duration(iv pgtype.Interval) (time.Duration, error) {
	if iv.Months != 0 {
		return 0, errors.New("use days or hours, not months")
	}
	return time.Duration(iv.Days)*24*time.Hour + time.Duration(iv.Microseconds)*time.Microsecond, nil
}

type Partition struct {
	ID       int64
	Start    time.Time
	End      time.Time
	Tier     string
	State    string
	Location string
}

// BucketStart returns the start of the time bucket that t falls in.
func BucketStart(t time.Time, size time.Duration) time.Time {
	return t.UTC().Truncate(size)
}

// IndexName returns the OpenSearch index for the bucket starting at start.
func IndexName(start time.Time, size time.Duration) string {
	if size < 24*time.Hour {
		return start.UTC().Format("logs-2006.01.02.15")
	}
	return start.UTC().Format("logs-2006.01.02")
}

// EnsurePartition returns the partition for the bucket starting at start,
// creating it as an OPEN partition in the HOT tier if it does not exist yet.
func (c *Catalog) EnsurePartition(ctx context.Context, start time.Time, size time.Duration) (Partition, error) {
	_, err := c.db.Exec(ctx,
		`INSERT INTO data_partitions (bucket_start, bucket_end, location)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (bucket_start) DO NOTHING`,
		start, start.Add(size), IndexName(start, size))
	if err != nil {
		return Partition{}, err
	}

	var p Partition
	err = c.db.QueryRow(ctx,
		`SELECT partition_id, bucket_start, bucket_end, tier, state, location
		   FROM data_partitions WHERE bucket_start = $1`, start,
	).Scan(&p.ID, &p.Start, &p.End, &p.Tier, &p.State, &p.Location)
	return p, err
}

// TouchPartition records that data was just written to an open partition.
// dlm only seals a partition after a quiet period with no writes.
func (c *Catalog) TouchPartition(ctx context.Context, id int64) error {
	_, err := c.db.Exec(ctx,
		`UPDATE data_partitions SET last_write_at = now(), updated_at = now()
		  WHERE partition_id = $1 AND state = 'OPEN'`, id)
	return err
}

// Partitions returns every partition that can still be read, oldest first.
func (c *Catalog) Partitions(ctx context.Context) ([]Partition, error) {
	rows, err := c.db.Query(ctx,
		`SELECT partition_id, bucket_start, bucket_end, tier, state, location
		   FROM data_partitions WHERE state <> 'DELETED' ORDER BY bucket_start`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Partition, error) {
		var p Partition
		err := row.Scan(&p.ID, &p.Start, &p.End, &p.Tier, &p.State, &p.Location)
		return p, err
	})
}
