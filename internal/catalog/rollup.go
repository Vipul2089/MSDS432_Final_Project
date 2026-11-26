package catalog

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// HourlyCount is one row of obs_gold.fact_events_hourly.
type HourlyCount struct {
	Hour             time.Time
	Source, Category string
	Events, Errors   int64
	Bytes            int64
}

// RollupCandidates returns the partitions whose hourly counts may be out of
// date: open ones, which are still changing, and sealed ones that have not
// had their final rollup. Both are still in OpenSearch.
func (c *Catalog) RollupCandidates(ctx context.Context) ([]Partition, error) {
	rows, err := c.db.Query(ctx,
		`SELECT partition_id, bucket_start, bucket_end, tier, state, location
		   FROM data_partitions
		  WHERE tier IN ('HOT', 'WARM')
		    AND (state = 'OPEN' OR (state = 'SEALED' AND gold_final_at IS NULL))
		  ORDER BY bucket_start`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Partition, error) {
		var p Partition
		err := row.Scan(&p.ID, &p.Start, &p.End, &p.Tier, &p.State, &p.Location)
		return p, err
	})
}

// SaveRollup replaces the partition's hours in fact_events_hourly. For a
// sealed partition it also sets gold_final_at, which allows the move to COLD.
func (c *Catalog) SaveRollup(ctx context.Context, p Partition, counts []HourlyCount) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`DELETE FROM obs_gold.fact_events_hourly WHERE hour_start >= $1 AND hour_start < $2`,
		p.Start, p.End); err != nil {
		return err
	}
	rows := make([][]any, len(counts))
	for i, r := range counts {
		rows[i] = []any{r.Hour, r.Source, r.Category, r.Events, r.Errors, r.Bytes}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"obs_gold", "fact_events_hourly"},
		[]string{"hour_start", "source", "category", "event_count", "error_count", "bytes_total"},
		pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	if p.State == "SEALED" {
		if _, err := tx.Exec(ctx,
			`UPDATE data_partitions SET gold_final_at = now() WHERE partition_id = $1 AND state = 'SEALED'`,
			p.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
