package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// BlockWrites makes an index read-only. Later bulk writes to it fail.
func (c *Client) BlockWrites(ctx context.Context, index string) error {
	_, err := c.do(ctx, http.MethodPut, "/"+index+"/_settings", "application/json",
		[]byte(`{"index.blocks.write":true}`))
	return err
}

func (c *Client) Refresh(ctx context.Context, index string) error {
	_, err := c.do(ctx, http.MethodPost, "/"+index+"/_refresh", "", nil)
	return err
}

func (c *Client) Count(ctx context.Context, index string) (int64, error) {
	data, err := c.do(ctx, http.MethodGet, "/"+index+"/_count", "", nil)
	if err != nil {
		return 0, err
	}
	var r struct{ Count int64 }
	err = json.Unmarshal(data, &r)
	return r.Count, err
}

// StoreBytes returns the disk size of the index's primary shards.
func (c *Client) StoreBytes(ctx context.Context, index string) (int64, error) {
	data, err := c.do(ctx, http.MethodGet, "/"+index+"/_stats/store", "", nil)
	if err != nil {
		return 0, err
	}
	var r struct {
		All struct {
			Primaries struct {
				Store struct {
					SizeInBytes int64 `json:"size_in_bytes"`
				} `json:"store"`
			} `json:"primaries"`
		} `json:"_all"`
	}
	err = json.Unmarshal(data, &r)
	return r.All.Primaries.Store.SizeInBytes, err
}

// ForceMerge merges each shard of the index into one segment. It blocks until
// the merge finishes, so ctx should carry a generous deadline.
func (c *Client) ForceMerge(ctx context.Context, index string) error {
	_, err := c.doWith(ctx, c.slow, http.MethodPost, "/"+index+"/_forcemerge?max_num_segments=1", "", nil)
	return err
}

func (c *Client) SetReplicas(ctx context.Context, index string, n int) error {
	_, err := c.do(ctx, http.MethodPut, "/"+index+"/_settings", "application/json",
		fmt.Appendf(nil, `{"index.number_of_replicas":%d}`, n))
	return err
}

// WaitGreen waits until every shard of the index is allocated.
func (c *Client) WaitGreen(ctx context.Context, index string, timeout time.Duration) error {
	path := fmt.Sprintf("/_cluster/health/%s?wait_for_status=green&timeout=%ds", index, int(timeout.Seconds()))
	data, err := c.doWith(ctx, c.slow, http.MethodGet, path, "", nil)
	if err != nil {
		return err
	}
	var r struct {
		TimedOut bool `json:"timed_out"`
		Status   string
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if r.TimedOut {
		return fmt.Errorf("index %s is %s after %s", index, r.Status, timeout)
	}
	return nil
}

// DeleteIndex deletes an index. An index that is already gone is not an error.
func (c *Client) DeleteIndex(ctx context.Context, index string) error {
	_, err := c.do(ctx, http.MethodDelete, "/"+index, "", nil)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}

// OpenPIT opens a point in time on the index, so paging through it sees one
// consistent view of the data.
func (c *Client) OpenPIT(ctx context.Context, index string, keepAlive time.Duration) (string, error) {
	data, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/%s/_search/point_in_time?keep_alive=%ds", index, int(keepAlive.Seconds())), "", nil)
	if err != nil {
		return "", err
	}
	var r struct {
		PitID string `json:"pit_id"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", err
	}
	return r.PitID, nil
}

func (c *Client) ClosePIT(ctx context.Context, id string) error {
	body, _ := json.Marshal(map[string][]string{"pit_id": {id}})
	_, err := c.do(ctx, http.MethodDelete, "/_search/point_in_time", "application/json", body)
	return err
}

// PageHit is one document from a PIT page. Sort is passed back as
// search_after to fetch the next page.
type PageHit struct {
	Source json.RawMessage `json:"_source"`
	Sort   json.RawMessage `json:"sort"`
}

// Page returns the next size documents of the PIT after the given sort
// values (nil for the first page), ordered by event_time then event_id.
func (c *Client) Page(ctx context.Context, pitID string, keepAlive time.Duration, after json.RawMessage, size int) ([]PageHit, error) {
	req := map[string]any{
		"size":             size,
		"pit":              map[string]any{"id": pitID, "keep_alive": fmt.Sprintf("%ds", int(keepAlive.Seconds()))},
		"sort":             []any{map[string]string{"event_time": "asc"}, map[string]string{"event_id": "asc"}},
		"track_total_hits": false,
	}
	if after != nil {
		req["search_after"] = after
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data, err := c.do(ctx, http.MethodPost, "/_search", "application/json", body)
	if err != nil {
		return nil, err
	}
	var r struct {
		Hits struct {
			Hits []PageHit `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return r.Hits.Hits, nil
}
