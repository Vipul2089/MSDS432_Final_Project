package dlm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"obsfabric/internal/catalog"
)

// hourlyQuery counts a partition's events per hour, source and category,
// with error counts and bytes. One shard per index makes terms counts exact.
var hourlyQuery = []byte(`{
  "size": 0,
  "track_total_hits": false,
  "aggs": {
    "hours": {
      "date_histogram": {"field": "event_time", "fixed_interval": "1h", "time_zone": "UTC", "min_doc_count": 1},
      "aggs": {
        "source": {
          "terms": {"field": "source", "size": 10},
          "aggs": {
            "category": {
              "terms": {"field": "category", "size": 50},
              "aggs": {
                "bytes": {"sum": {"field": "bytes"}},
                "errors": {"filter": {"terms": {"severity": ["ERROR", "CRITICAL"]}}}
              }
            }
          }
        }
      }
    }
  }
}`)

// rollup keeps obs_gold.fact_events_hourly up to date for partitions still
// in OpenSearch (LLD-001 §6.5). Failures are retried on the next cycle.
func (s *Service) rollup(ctx context.Context) {
	parts, err := s.cat.RollupCandidates(ctx)
	if err != nil {
		s.log.Warn("find partitions to roll up", "err", err)
		return
	}
	for _, p := range parts {
		data, err := s.search.Search(ctx, []string{p.Location}, hourlyQuery)
		if err != nil {
			s.log.Warn("hourly counts", "partition_id", p.ID, "err", err)
			continue
		}
		counts, err := parseHourly(data)
		if err != nil {
			s.log.Warn("hourly counts", "partition_id", p.ID, "err", err)
			continue
		}
		if err := s.cat.SaveRollup(ctx, p, counts); err != nil {
			s.log.Warn("save hourly counts", "partition_id", p.ID, "err", err)
			continue
		}
		if p.State == "SEALED" {
			s.log.Info("final hourly rollup saved", "partition_id", p.ID, "rows", len(counts))
		}
	}
}

func parseHourly(data []byte) ([]catalog.HourlyCount, error) {
	type bucket struct {
		Key      json.RawMessage `json:"key"`
		DocCount int64           `json:"doc_count"`
	}
	var resp struct {
		Aggregations struct {
			Hours struct {
				Buckets []struct {
					Key    int64 `json:"key"` // epoch milliseconds
					Source struct {
						Buckets []struct {
							bucket
							Category struct {
								Buckets []struct {
									bucket
									Bytes  struct{ Value float64 } `json:"bytes"`
									Errors struct {
										DocCount int64 `json:"doc_count"`
									} `json:"errors"`
								} `json:"buckets"`
							} `json:"category"`
						} `json:"buckets"`
					} `json:"source"`
				} `json:"buckets"`
			} `json:"hours"`
		} `json:"aggregations"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode hourly counts: %w", err)
	}

	var out []catalog.HourlyCount
	for _, h := range resp.Aggregations.Hours.Buckets {
		hour := time.UnixMilli(h.Key).UTC()
		for _, src := range h.Source.Buckets {
			var source string
			if err := json.Unmarshal(src.Key, &source); err != nil {
				return nil, err
			}
			for _, cat := range src.Category.Buckets {
				var category string
				if err := json.Unmarshal(cat.Key, &category); err != nil {
					return nil, err
				}
				out = append(out, catalog.HourlyCount{
					Hour: hour, Source: source, Category: category,
					Events: cat.DocCount, Errors: cat.Errors.DocCount, Bytes: int64(cat.Bytes.Value),
				})
			}
		}
	}
	return out, nil
}
