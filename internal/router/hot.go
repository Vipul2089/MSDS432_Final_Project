package router

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// hotRequest builds the OpenSearch query for the HOT and WARM tiers.
// Filters do not affect scoring and can be cached by OpenSearch; the search
// text goes in "must" and "must_not".
func hotRequest(q Query, timeout time.Duration) ([]byte, error) {
	filter := []any{
		map[string]any{"range": map[string]any{
			"event_time": map[string]any{"gte": q.From, "lt": q.To},
		}},
	}
	if q.EventID != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"event_id": q.EventID}})
	}
	if len(q.Sources) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"source": q.Sources}})
	}
	if len(q.Severities) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"severity": q.Severities}})
	}

	boolQuery := map[string]any{"filter": filter}
	must, mustNot := q.terms.hot()
	if len(must) > 0 {
		boolQuery["must"] = must
	}
	if len(mustNot) > 0 {
		boolQuery["must_not"] = mustNot
	}

	return json.Marshal(map[string]any{
		"query":            map[string]any{"bool": boolQuery},
		"sort":             []any{map[string]any{"event_time": "desc"}},
		"size":             q.Limit,
		"_source":          []string{"event_id", "event_time", "source", "severity", "raw_message"},
		"timeout":          fmt.Sprintf("%dms", timeout.Milliseconds()),
		"track_total_hits": false,
	})
}

// searchHot runs one search over the indices of the HOT and WARM partitions.
// timedOut is true when OpenSearch stopped early and the hits are incomplete.
func (r *Router) searchHot(ctx context.Context, q Query, indices []string) (hits []Hit, timedOut bool, err error) {
	body, err := hotRequest(q, r.cfg.HotTimeout)
	if err != nil {
		return nil, false, err
	}
	// Leave OpenSearch time to return partial results before giving up.
	ctx, cancel := context.WithTimeout(ctx, r.cfg.HotTimeout+time.Second)
	defer cancel()

	data, err := r.search.Search(ctx, indices, body)
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		TimedOut bool `json:"timed_out"`
		Hits     struct {
			Hits []struct {
				Index  string `json:"_index"`
				Source Hit    `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("decode search response: %w", err)
	}
	hits = make([]Hit, len(resp.Hits.Hits))
	for i, h := range resp.Hits.Hits {
		hits[i] = h.Source
		hits[i].index = h.Index
	}
	return hits, resp.TimedOut, nil
}
