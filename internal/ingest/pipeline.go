package ingest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/sync/errgroup"

	"obsfabric/internal/catalog"
	"obsfabric/internal/event"
	"obsfabric/internal/search"
	"obsfabric/internal/source/nslkdd"
)

const (
	queueSize      = 10_000
	maxRecordBytes = 64 << 10
	bulkBytes      = 5 << 20
	flushInterval  = time.Second
	touchInterval  = 10 * time.Second
	futureSkew     = 5 * time.Minute
	maxAttempts    = 5
)

var (
	eventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ingest_events_total",
		Help: "Records processed, by outcome: loaded, duplicate or rejected.",
	}, []string{"source", "outcome"})
	bulkSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ingest_bulk_duration_seconds",
		Help:    "Duration of bulk requests to OpenSearch.",
		Buckets: prometheus.DefBuckets,
	})
)

// record is one line of an input file. offset is its zero-based line number.
type record struct {
	offset int64
	line   string
}

type parsed struct {
	record
	ev  event.Event
	err error
}

// batch is a group of documents sent in one bulk request. docs[i] holds the
// action and source lines for recs[i].
type batch struct {
	docs [][]byte
	recs []record
	size int
}

type fileStats struct {
	read, loaded, duplicate atomic.Int64

	mu      sync.Mutex
	rejects []catalog.Reject
}

func (s *fileStats) reject(r record, reason, detail string) {
	eventsTotal.WithLabelValues(nslkdd.Source, "rejected").Inc()
	s.mu.Lock()
	s.rejects = append(s.rejects, catalog.Reject{Offset: r.offset, Reason: reason, Detail: detail, Raw: r.line})
	s.mu.Unlock()
}

// load runs the pipeline for one file:
//
//	scan -> parse workers -> route (partition lookup, batching) -> bulk writers
//
// Channels between the stages are bounded, so a slow OpenSearch makes the
// whole pipeline slow down instead of growing memory (backpressure).
func (in *Ingester) load(ctx context.Context, path, origin string, batchID int64, policy catalog.Policy, st *fileStats) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	g, ctx := errgroup.WithContext(ctx)
	records := make(chan record, queueSize)
	results := make(chan parsed, queueSize)
	batches := make(chan *batch, in.cfg.BulkWorkers)

	g.Go(func() error {
		defer close(records)
		return scan(ctx, f, records, st)
	})

	var parsers sync.WaitGroup
	for range in.cfg.Workers {
		parsers.Add(1)
		g.Go(func() error {
			defer parsers.Done()
			for r := range records {
				ev, err := nslkdd.Parse(r.line)
				if err == nil {
					ev.ID = event.ID(nslkdd.Source, origin, r.offset)
				}
				select {
				case results <- parsed{record: r, ev: ev, err: err}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}
	go func() {
		parsers.Wait()
		close(results)
	}()

	g.Go(func() error {
		defer close(batches)
		return in.route(ctx, results, batches, batchID, policy, st)
	})

	for range in.cfg.BulkWorkers {
		g.Go(func() error {
			for b := range batches {
				if err := in.flush(ctx, b, st); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return g.Wait()
}

func scan(ctx context.Context, r io.Reader, out chan<- record, st *fileStats) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for offset := int64(0); sc.Scan(); offset++ {
		rec := record{offset: offset, line: sc.Text()}
		if rec.line == "" {
			continue
		}
		st.read.Add(1)
		if len(rec.line) > maxRecordBytes {
			st.reject(rec, event.ReasonTooLarge, fmt.Sprintf("%d bytes", len(rec.line)))
			continue
		}
		select {
		case out <- rec:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sc.Err()
}

// route checks event times against the policy, finds each event's partition
// and groups documents into bulk batches. It runs in a single goroutine, so
// its partition cache needs no locking.
func (in *Ingester) route(ctx context.Context, results <-chan parsed, out chan<- *batch, batchID int64, policy catalog.Policy, st *fileStats) error {
	parts := map[time.Time]catalog.Partition{}
	touched := map[int64]time.Time{}
	cur := &batch{}

	send := func() error {
		if len(cur.docs) == 0 {
			return nil
		}
		select {
		case out <- cur:
			cur = &batch{}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
		case p, ok := <-results:
			if !ok {
				return send()
			}
			var rej *event.RejectError
			if errors.As(p.err, &rej) {
				st.reject(p.record, rej.Reason, rej.Detail)
				continue
			} else if p.err != nil {
				return p.err
			}

			now := time.Now().UTC()
			ev := p.ev
			if ev.EventTime.After(now.Add(futureSkew)) {
				st.reject(p.record, event.ReasonFuture, ev.EventTime.Format(time.RFC3339))
				continue
			}
			if ev.EventTime.Before(now.Add(-policy.RetainFor)) {
				st.reject(p.record, event.ReasonExpired, ev.EventTime.Format(time.RFC3339))
				continue
			}

			start := catalog.BucketStart(ev.EventTime, policy.BucketSize)
			part, ok := parts[start]
			if !ok {
				var err error
				if part, err = in.openPartition(ctx, start, policy.BucketSize); err != nil {
					return err
				}
				parts[start] = part
			}
			if part.State != "OPEN" {
				st.reject(p.record, event.ReasonLate, "partition "+part.Location+" is "+part.State)
				continue
			}
			if now.Sub(touched[part.ID]) > touchInterval {
				if err := in.catalog.TouchPartition(ctx, part.ID); err != nil {
					return err
				}
				touched[part.ID] = now
			}

			ev.PartitionID, ev.BatchID, ev.ObservedTime = part.ID, batchID, now
			doc, err := bulkDoc(part.Location, ev)
			if err != nil {
				return err
			}
			cur.docs = append(cur.docs, doc)
			cur.recs = append(cur.recs, p.record)
			cur.size += len(doc)
			if cur.size >= bulkBytes {
				if err := send(); err != nil {
					return err
				}
			}
		}
	}
}

// openPartition registers the bucket in the catalog before creating its
// index, so no document can exist without a catalog entry (INV-1).
func (in *Ingester) openPartition(ctx context.Context, start time.Time, size time.Duration) (catalog.Partition, error) {
	p, err := in.catalog.EnsurePartition(ctx, start, size)
	if err != nil {
		return p, fmt.Errorf("register partition: %w", err)
	}
	if p.State == "OPEN" {
		if err := in.search.CreateIndex(ctx, p.Location); err != nil {
			return p, fmt.Errorf("create index %s: %w", p.Location, err)
		}
	}
	return p, nil
}

// bulkDoc encodes a create action and the event. "create" fails with 409 if
// the ID already exists, which is how repeated loads are detected.
func bulkDoc(index string, ev event.Event) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"create":{"_index":%q,"_id":%q}}`+"\n", index, ev.ID)
	if err := json.NewEncoder(&buf).Encode(ev); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// flush sends a batch, retrying the whole request on server or network
// errors and only the throttled documents on 429.
func (in *Ingester) flush(ctx context.Context, b *batch, st *fileStats) error {
	pending := make([]int, len(b.docs))
	for i := range pending {
		pending[i] = i
	}

	for attempt := 1; ; attempt++ {
		body := make([]byte, 0, b.size)
		for _, i := range pending {
			body = append(body, b.docs[i]...)
		}

		start := time.Now()
		items, err := in.search.Bulk(ctx, body)
		bulkSeconds.Observe(time.Since(start).Seconds())
		if err == nil && len(items) != len(pending) {
			err = fmt.Errorf("bulk returned %d items for %d documents", len(items), len(pending))
		}
		if err != nil {
			if attempt >= maxAttempts || !search.Retryable(err) {
				return fmt.Errorf("bulk request: %w", err)
			}
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}

		var retry []int
		for k, it := range items {
			i := pending[k]
			switch {
			case it.Status == 201:
				st.loaded.Add(1)
				eventsTotal.WithLabelValues(nslkdd.Source, "loaded").Inc()
			case it.Status == 409:
				st.duplicate.Add(1)
				eventsTotal.WithLabelValues(nslkdd.Source, "duplicate").Inc()
			case it.Status == 429:
				retry = append(retry, i)
			case it.Status == 400:
				st.reject(b.recs[i], event.ReasonMapping, it.Error)
			default:
				st.reject(b.recs[i], event.ReasonIndexError, it.Error)
			}
		}
		if len(retry) == 0 {
			return nil
		}
		if attempt >= maxAttempts {
			return fmt.Errorf("%d documents still throttled after %d attempts", len(retry), attempt)
		}
		pending = retry
		if err := sleep(ctx, backoff(attempt)); err != nil {
			return err
		}
	}
}

// backoff doubles from 200ms with up to 50% random jitter.
func backoff(attempt int) time.Duration {
	d := 200 * time.Millisecond << (attempt - 1)
	return d + rand.N(d/2)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
