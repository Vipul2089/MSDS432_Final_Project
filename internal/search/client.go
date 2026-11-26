// Package search is a small client for the OpenSearch REST endpoints we use.
package search

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:embed template.json
var indexTemplate []byte

type Client struct {
	url  string
	http *http.Client
	slow *http.Client // no timeout of its own; for calls bounded by the caller's context
}

func New(url string) *Client {
	return &Client{
		url:  strings.TrimRight(url, "/"),
		http: &http.Client{Timeout: 60 * time.Second},
		slow: &http.Client{},
	}
}

// StatusError is returned when OpenSearch answers with an unexpected status.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("opensearch: status %d: %s", e.Code, e.Body) }

// Retryable reports whether a failed request is worth retrying: the cluster
// was busy (429), had a server error, or could not be reached.
func Retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return true
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte) ([]byte, error) {
	return c.doWith(ctx, c.http, method, path, contentType, body)
}

func (c *Client) doWith(ctx context.Context, hc *http.Client, method, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return data, &StatusError{Code: resp.StatusCode, Body: string(data)}
	}
	return data, nil
}

// Ping fails if the cluster is unreachable or its health is red.
func (c *Client) Ping(ctx context.Context) error {
	data, err := c.do(ctx, http.MethodGet, "/_cluster/health", "", nil)
	if err != nil {
		return err
	}
	var h struct{ Status string }
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
	if h.Status == "red" {
		return errors.New("cluster health is red")
	}
	return nil
}

// EnsureTemplate installs the index template applied to every logs-* index.
func (c *Client) EnsureTemplate(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPut, "/_index_template/logs", "application/json", indexTemplate)
	return err
}

// CreateIndex creates an index. An index that already exists is not an error.
func (c *Client) CreateIndex(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodPut, "/"+name, "", nil)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusBadRequest && strings.Contains(se.Body, "resource_already_exists_exception") {
		return nil
	}
	return err
}

// Search runs a query over the given indices and returns the raw response.
// Missing indices are skipped: a partition can be moved or deleted between
// reading the catalog and running the search.
func (c *Client) Search(ctx context.Context, indices []string, body []byte) ([]byte, error) {
	path := "/" + strings.Join(indices, ",") + "/_search?ignore_unavailable=true&allow_no_indices=true"
	return c.do(ctx, http.MethodPost, path, "application/json", body)
}

// BulkItem is the outcome of one action in a bulk request.
type BulkItem struct {
	Status int
	Error  string
}

// Bulk sends an NDJSON body to the _bulk endpoint and returns one item per
// action, in request order.
func (c *Client) Bulk(ctx context.Context, body []byte) ([]BulkItem, error) {
	data, err := c.do(ctx, http.MethodPost, "/_bulk", "application/x-ndjson", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Items []map[string]struct {
			Status int `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode bulk response: %w", err)
	}
	items := make([]BulkItem, len(resp.Items))
	for i, item := range resp.Items {
		for _, r := range item { // a single key: the action name, e.g. "create"
			items[i].Status = r.Status
			if r.Error != nil {
				items[i].Error = r.Error.Type + ": " + r.Error.Reason
			}
		}
	}
	return items, nil
}
