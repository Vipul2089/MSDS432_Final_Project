// Package ingest loads log files from the inbox folder into the HOT tier.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"obsfabric/internal/catalog"
	"obsfabric/internal/objectstore"
	"obsfabric/internal/search"
	"obsfabric/internal/source/nslkdd"
)

const (
	pollInterval = 5 * time.Second
	maxFiles     = 2   // files processed at the same time
	maxRejectPct = 1.0 // above this share of rejected rows a batch is PARTIAL
	putTimeout   = 5 * time.Minute
)

var filesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "ingest_files_total",
	Help: "Input files processed, by final batch status.",
}, []string{"status"})

type Config struct {
	Inbox       string
	Workers     int // parser goroutines per file
	BulkWorkers int // concurrent bulk requests per file
}

type Ingester struct {
	cfg     Config
	log     *slog.Logger
	catalog *catalog.Catalog
	search  *search.Client
	store   *objectstore.Store

	mu       sync.Mutex
	inflight map[string]bool
}

func New(cfg Config, log *slog.Logger, cat *catalog.Catalog, sc *search.Client, store *objectstore.Store) *Ingester {
	return &Ingester{cfg: cfg, log: log, catalog: cat, search: sc, store: store, inflight: map[string]bool{}}
}

// Run polls the inbox until ctx is cancelled, then waits for files in
// progress to stop.
func (in *Ingester) Run(ctx context.Context) error {
	n, err := in.catalog.FailRunningBatches(ctx)
	if err != nil {
		return fmt.Errorf("close interrupted batches: %w", err)
	}
	if n > 0 {
		in.log.Warn("marked interrupted batches as FAILED", "count", n)
	}
	for _, dir := range []string{"done", "failed"} {
		if err := os.MkdirAll(filepath.Join(in.cfg.Inbox, dir), 0o755); err != nil {
			return err
		}
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := in.poll(ctx, &wg); err != nil {
			in.log.Error("read inbox", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (in *Ingester) poll(ctx context.Context, wg *sync.WaitGroup) error {
	entries, err := os.ReadDir(in.cfg.Inbox) // sorted by name, so files load in order
	if err != nil {
		return err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".tmp") || in.inflight[name] {
			continue
		}
		if len(in.inflight) >= maxFiles {
			return nil
		}
		in.inflight[name] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			in.handle(ctx, name)
			in.mu.Lock()
			delete(in.inflight, name)
			in.mu.Unlock()
		}()
	}
	return nil
}

// handle processes one file and moves it to done/ or failed/. A file that
// was interrupted by shutdown stays in the inbox and is loaded again on the
// next start; event IDs make that safe.
func (in *Ingester) handle(ctx context.Context, name string) {
	path := filepath.Join(in.cfg.Inbox, name)
	log := in.log.With("file", name)

	status, err := in.processFile(ctx, path, log)
	filesTotal.WithLabelValues(status).Inc()
	if ctx.Err() != nil {
		log.Warn("interrupted, file left in inbox")
		return
	}
	dest := "done"
	if err != nil {
		log.Error("file failed", "err", err)
		dest = "failed"
	}
	if err := os.Rename(path, filepath.Join(in.cfg.Inbox, dest, name)); err != nil {
		log.Error("move file", "err", err)
	}
}

func (in *Ingester) processFile(ctx context.Context, path string, log *slog.Logger) (status string, err error) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, nslkdd.Source+"-") {
		return "FAILED", fmt.Errorf("no parser for %s", name)
	}

	sum, err := fileSHA256(path)
	if err != nil {
		return "FAILED", err
	}
	sourceID, err := in.catalog.SourceID(ctx, nslkdd.Source)
	if err != nil {
		return "FAILED", fmt.Errorf("look up source: %w", err)
	}
	policy, err := in.catalog.ActivePolicy(ctx)
	if err != nil {
		return "FAILED", err
	}
	batchID, err := in.catalog.StartBatch(ctx, sourceID, name, sum)
	if err != nil {
		return "FAILED", fmt.Errorf("start batch: %w", err)
	}
	log = log.With("batch_id", batchID)
	start := time.Now()

	res := catalog.BatchResult{Status: "FAILED"}
	st := &fileStats{}
	defer func() {
		// Record the outcome even if ctx was cancelled.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		res.Read, res.Loaded, res.Duplicate = st.read.Load(), st.loaded.Load(), st.duplicate.Load()
		res.Rejected = int64(len(st.rejects))
		if err := in.catalog.AddRejects(bg, batchID, st.rejects); err != nil {
			log.Error("save rejected records", "err", err)
		}
		if err := in.catalog.FinishBatch(bg, batchID, res); err != nil {
			log.Error("finish batch", "err", err)
		}
		status = res.Status
		log.Info("batch finished", "status", res.Status, "read", res.Read, "loaded", res.Loaded,
			"duplicate", res.Duplicate, "rejected", res.Rejected, "took", time.Since(start).Round(time.Millisecond).String())
	}()

	key := fmt.Sprintf("bronze/source=%s/date=%s/%s.csv", nslkdd.Source, time.Now().UTC().Format("2006-01-02"), sum)
	putCtx, cancel := context.WithTimeout(ctx, putTimeout)
	defer cancel()
	if err := in.store.PutFile(putCtx, key, path); err != nil {
		return "", fmt.Errorf("copy to bronze: %w", err)
	}
	res.BronzeURI = in.store.URI(key)

	if err := in.load(ctx, path, sum, batchID, policy, st); err != nil {
		return "", err
	}
	res.Status = "SUCCESS"
	if read := st.read.Load(); read > 0 && float64(len(st.rejects))/float64(read)*100 > maxRejectPct {
		res.Status = "PARTIAL"
	}
	return "", nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
