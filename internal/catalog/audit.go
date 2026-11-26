package catalog

import (
	"context"
	"time"
)

// QueryAudit is one row of query_audit, written for every search.
type QueryAudit struct {
	ID            [16]byte
	Text          string
	From, To      time.Time
	Tiers         []string
	Planned       int
	Failed        int
	Results       int
	Duration      time.Duration
	TierDurations map[string]int64 // milliseconds per tier
	Status        string           // COMPLETE, PARTIAL or FAILED
}

func (c *Catalog) AddQueryAudit(ctx context.Context, a QueryAudit) error {
	_, err := c.db.Exec(ctx,
		`INSERT INTO query_audit (query_id, query_text, time_from, time_to, tiers, partitions_planned,
		                          partitions_failed, result_count, duration_ms, tier_durations, status)
		 VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		a.ID, a.Text, a.From, a.To, a.Tiers, a.Planned, a.Failed, a.Results,
		a.Duration.Milliseconds(), a.TierDurations, a.Status)
	return err
}
