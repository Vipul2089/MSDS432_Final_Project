package catalog

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (c *Catalog) SourceID(ctx context.Context, name string) (int16, error) {
	var id int16
	err := c.db.QueryRow(ctx, `SELECT source_id FROM log_sources WHERE source_name = $1`, name).Scan(&id)
	return id, err
}

// StartBatch records that a file is being loaded and returns the batch ID.
func (c *Catalog) StartBatch(ctx context.Context, sourceID int16, fileName, sha256 string) (int64, error) {
	var id int64
	err := c.db.QueryRow(ctx,
		`INSERT INTO ingest_batches (source_id, file_name, file_sha256)
		 VALUES ($1, $2, $3) RETURNING batch_id`,
		sourceID, fileName, sha256,
	).Scan(&id)
	return id, err
}

// FailRunningBatches marks batches left RUNNING by a previous process as
// FAILED. Only one ingest instance runs, so at startup none can be in progress.
func (c *Catalog) FailRunningBatches(ctx context.Context) (int64, error) {
	tag, err := c.db.Exec(ctx,
		`UPDATE ingest_batches SET status = 'FAILED', finished_at = now() WHERE status = 'RUNNING'`)
	return tag.RowsAffected(), err
}

type BatchResult struct {
	BronzeURI string
	Read      int64
	Loaded    int64
	Rejected  int64
	Duplicate int64
	Status    string // SUCCESS, PARTIAL or FAILED
}

func (c *Catalog) FinishBatch(ctx context.Context, id int64, r BatchResult) error {
	_, err := c.db.Exec(ctx,
		`UPDATE ingest_batches
		    SET bronze_uri = NULLIF($2, ''), rows_read = $3, rows_loaded = $4,
		        rows_rejected = $5, rows_duplicate = $6, status = $7, finished_at = now()
		  WHERE batch_id = $1`,
		id, r.BronzeURI, r.Read, r.Loaded, r.Rejected, r.Duplicate, r.Status)
	return err
}

type Reject struct {
	Offset int64
	Reason string
	Detail string
	Raw    string
}

// maxRawBytes limits how much of a rejected record is stored. The full
// record is still in the Bronze copy of the file.
const maxRawBytes = 2048

func (c *Catalog) AddRejects(ctx context.Context, batchID int64, rejects []Reject) error {
	if len(rejects) == 0 {
		return nil
	}
	rows := make([][]any, len(rejects))
	for i, r := range rejects {
		raw := r.Raw
		if len(raw) > maxRawBytes {
			raw = raw[:maxRawBytes]
		}
		rows[i] = []any{batchID, r.Offset, r.Reason, r.Detail, raw}
	}
	_, err := c.db.CopyFrom(ctx,
		pgx.Identifier{"rejected_records"},
		[]string{"batch_id", "record_offset", "reason", "detail", "raw_record"},
		pgx.CopyFromRows(rows))
	return err
}
