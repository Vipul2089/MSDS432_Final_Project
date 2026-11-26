// Package router serves the search API. It reads the catalog to find which
// tier holds each part of the requested time range and searches each tier.
package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"obsfabric/internal/catalog"
	"obsfabric/internal/cold"
	"obsfabric/internal/objectstore"
	"obsfabric/internal/search"
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_requests_total",
		Help: "Search requests, by result status.",
	}, []string{"status"})
	tierSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "router_tier_duration_seconds",
		Help:    "Time spent searching each tier.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tier"})
	tierErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_tier_errors_total",
		Help: "Tier searches that failed or timed out.",
	}, []string{"tier", "reason"})
)

type Config struct {
	HotTimeout    time.Duration
	ColdTimeout   time.Duration
	MaxColdWindow time.Duration // widest time span a search may cover in COLD
	CatalogTTL    time.Duration
}

type Router struct {
	cfg    Config
	log    *slog.Logger
	search *search.Client
	store  *objectstore.Store
	duck   *sql.DB // DuckDB pool for COLD queries; its size limits concurrent COLD searches
	plan   *planner
	audit  *auditor

	mu        sync.Mutex
	manifests map[string]cold.Manifest // by URI; manifests never change once written
}

func New(cfg Config, log *slog.Logger, cat *catalog.Catalog, sc *search.Client, store *objectstore.Store, duck *sql.DB) *Router {
	return &Router{
		cfg:       cfg,
		log:       log,
		search:    sc,
		store:     store,
		duck:      duck,
		plan:      &planner{cat: cat, ttl: cfg.CatalogTTL, log: log},
		audit:     newAuditor(cat, log),
		manifests: map[string]cold.Manifest{},
	}
}

// Run writes audit rows until ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
	return r.audit.run(ctx)
}

type Hit struct {
	EventID    string    `json:"event_id"`
	EventTime  time.Time `json:"event_time"`
	Tier       string    `json:"tier"`
	Source     string    `json:"source"`
	Severity   string    `json:"severity"`
	RawMessage string    `json:"raw_message"`

	index string
}

type TierResult struct {
	Tier       string `json:"tier"`
	Partitions int    `json:"partitions"`
	TookMS     int64  `json:"took_ms"`
	Hits       int    `json:"hits"`
	Status     string `json:"status"` // OK, TIMEOUT or ERROR
}

type Response struct {
	QueryID string       `json:"query_id"`
	Status  string       `json:"status"` // COMPLETE, PARTIAL or FAILED
	TookMS  int64        `json:"took_ms"`
	Tiers   []TierResult `json:"tiers"`
	Hits    []Hit        `json:"hits"`
}

// errColdWindow is returned when a search would scan too much COLD data.
var errColdWindow = errors.New("time range is too wide for COLD data")

// tierOutcome is what one search path returns: a result per tier it covered
// and the hits it found.
type tierOutcome struct {
	tiers []TierResult
	hits  []Hit
}

// Search plans the query against the catalog, searches the HOT/WARM and
// COLD tiers in parallel and merges the results. It returns an error only if
// the request cannot be planned.
func (r *Router) Search(ctx context.Context, q Query) (*Response, error) {
	start := time.Now()
	id := uuid.New()

	parts, err := r.plan.plan(ctx, q.From, q.To)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	var indexParts, coldParts []catalog.Partition
	for _, p := range parts {
		if p.Tier == "COLD" {
			coldParts = append(coldParts, p)
		} else {
			indexParts = append(indexParts, p)
		}
	}
	if span := coldSpan(q, coldParts); span > r.cfg.MaxColdWindow {
		return nil, fmt.Errorf("%w: it covers %s of COLD data, the limit is %s", errColdWindow, span, r.cfg.MaxColdWindow)
	}

	var hot, cld tierOutcome
	var wg sync.WaitGroup
	if len(indexParts) > 0 {
		wg.Go(func() { hot = r.searchIndexTiers(ctx, q, indexParts, id.String()) })
	}
	if len(coldParts) > 0 {
		wg.Go(func() { cld = r.searchColdTier(ctx, q, coldParts, id.String()) })
	}
	wg.Wait()

	resp := &Response{
		QueryID: id.String(),
		Tiers:   append(append([]TierResult{}, hot.tiers...), cld.tiers...),
		Hits:    merge(q.Limit, hot.hits, cld.hits),
	}
	for _, t := range resp.Tiers {
		tierSeconds.WithLabelValues(t.Tier).Observe(float64(t.TookMS) / 1000)
		if t.Status != "OK" {
			tierErrors.WithLabelValues(t.Tier, t.Status).Inc()
		}
	}
	resp.Status = overallStatus(resp.Tiers, len(resp.Hits))
	resp.TookMS = time.Since(start).Milliseconds()
	r.record(id, q, parts, resp)
	return resp, nil
}

// searchIndexTiers runs one OpenSearch query over the HOT and WARM
// partitions and reports a result for each of the two tiers.
func (r *Router) searchIndexTiers(ctx context.Context, q Query, parts []catalog.Partition, queryID string) tierOutcome {
	tierOf := map[string]string{}
	partsPerTier := map[string]int{}
	indices := make([]string, 0, len(parts))
	for _, p := range parts {
		indices = append(indices, p.Location)
		tierOf[p.Location] = p.Tier
		partsPerTier[p.Tier]++
	}

	t := time.Now()
	hits, timedOut, err := r.searchHot(ctx, q, indices)
	took := time.Since(t)

	status := "OK"
	switch {
	case errors.Is(err, context.DeadlineExceeded), err == nil && timedOut:
		status = "TIMEOUT"
	case err != nil:
		status = "ERROR"
	}
	if err != nil {
		r.log.Warn("hot search failed", "query_id", queryID, "err", err)
	}

	hitsPerTier := map[string]int{}
	for i := range hits {
		hits[i].Tier = tierOf[hits[i].index]
		hitsPerTier[hits[i].Tier]++
	}
	out := tierOutcome{hits: hits}
	for _, tier := range []string{"HOT", "WARM"} {
		if partsPerTier[tier] > 0 {
			out.tiers = append(out.tiers, TierResult{
				Tier: tier, Partitions: partsPerTier[tier], TookMS: took.Milliseconds(),
				Hits: hitsPerTier[tier], Status: status,
			})
		}
	}
	return out
}

func (r *Router) searchColdTier(ctx context.Context, q Query, parts []catalog.Partition, queryID string) tierOutcome {
	t := time.Now()
	ctx, cancel := context.WithTimeout(ctx, r.cfg.ColdTimeout)
	defer cancel()

	hits, err := r.searchCold(ctx, q, parts)
	status := "OK"
	if err != nil {
		status = "ERROR"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "TIMEOUT"
		}
		r.log.Warn("cold search failed", "query_id", queryID, "err", err)
		hits = nil
	}
	return tierOutcome{
		hits: hits,
		tiers: []TierResult{{
			Tier: "COLD", Partitions: len(parts), TookMS: time.Since(t).Milliseconds(),
			Hits: len(hits), Status: status,
		}},
	}
}

// coldSpan is how much of the requested range falls on COLD partitions.
// parts must be sorted by start time.
func coldSpan(q Query, parts []catalog.Partition) time.Duration {
	if len(parts) == 0 {
		return 0
	}
	from, to := q.From, q.To
	if s := parts[0].Start; s.After(from) {
		from = s
	}
	if e := parts[len(parts)-1].End; e.Before(to) {
		to = e
	}
	return to.Sub(from)
}

// merge combines the hits of all tiers newest first, keeps one hit per
// event ID and cuts the list to limit.
func merge(limit int, lists ...[]Hit) []Hit {
	var all []Hit
	for _, l := range lists {
		all = append(all, l...)
	}
	slices.SortStableFunc(all, func(a, b Hit) int {
		if c := b.EventTime.Compare(a.EventTime); c != 0 {
			return c
		}
		return strings.Compare(a.EventID, b.EventID)
	})

	out := make([]Hit, 0, min(limit, len(all)))
	seen := make(map[string]bool, len(all))
	for _, h := range all {
		if len(out) == limit {
			break
		}
		if !seen[h.EventID] {
			seen[h.EventID] = true
			out = append(out, h)
		}
	}
	return out
}

// overallStatus is COMPLETE when every tier answered, FAILED when none did
// and nothing was found, and PARTIAL otherwise.
func overallStatus(tiers []TierResult, hits int) string {
	failed := 0
	for _, t := range tiers {
		if t.Status != "OK" {
			failed++
		}
	}
	switch {
	case failed == 0:
		return "COMPLETE"
	case failed == len(tiers) && hits == 0:
		return "FAILED"
	default:
		return "PARTIAL"
	}
}

func (r *Router) record(id uuid.UUID, q Query, parts []catalog.Partition, resp *Response) {
	a := catalog.QueryAudit{
		ID: id, Text: q.Text, From: q.From, To: q.To,
		Tiers: []string{}, Planned: len(parts), Results: len(resp.Hits),
		Duration:      time.Duration(resp.TookMS) * time.Millisecond,
		TierDurations: map[string]int64{}, Status: resp.Status,
	}
	for _, t := range resp.Tiers {
		a.Tiers = append(a.Tiers, t.Tier)
		a.TierDurations[t.Tier] = t.TookMS
		if t.Status != "OK" {
			a.Failed += t.Partitions
		}
	}
	r.audit.add(a)
	r.log.Info("search", "query_id", resp.QueryID, "status", resp.Status, "partitions", len(parts),
		"hits", len(resp.Hits), "took_ms", resp.TookMS)
}

// ServeHTTP handles GET /api/v1/search.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	q, err := parseQuery(req.URL.Query())
	if err != nil {
		requestsTotal.WithLabelValues("INVALID").Inc()
		writeProblem(w, http.StatusBadRequest, "Invalid search request", err.Error(), "")
		return
	}

	resp, err := r.Search(req.Context(), q)
	if errors.Is(err, errColdWindow) {
		requestsTotal.WithLabelValues("INVALID").Inc()
		writeProblem(w, http.StatusBadRequest, "Invalid search request", err.Error(), "")
		return
	}
	if err != nil {
		requestsTotal.WithLabelValues("FAILED").Inc()
		r.log.Error("search failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Catalog unavailable",
			"the partition catalog could not be read", "")
		return
	}
	requestsTotal.WithLabelValues(resp.Status).Inc()
	if resp.Status == "FAILED" {
		writeProblem(w, http.StatusServiceUnavailable, "Search failed",
			"no tier could be searched", resp.QueryID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeProblem writes an RFC 9457 problem details response.
func writeProblem(w http.ResponseWriter, status int, title, detail, queryID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Type    string `json:"type"`
		Title   string `json:"title"`
		Status  int    `json:"status"`
		Detail  string `json:"detail"`
		QueryID string `json:"query_id,omitempty"`
	}{"about:blank", title, status, detail, queryID})
}
