// Package dlm moves partitions through the storage tiers (HOT, WARM, COLD)
// as they age. Each move is a job in migration_jobs whose status is saved
// after every step, so a restarted dlm continues where it stopped.
package dlm

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"obsfabric/internal/catalog"
	"obsfabric/internal/objectstore"
	"obsfabric/internal/search"
)

const lockRetry = 10 * time.Second

var lockHeld = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "dlm_lock_held",
	Help: "1 if this instance holds the dlm lock and is doing work.",
})

type Config struct {
	PlanInterval time.Duration
	RunInterval  time.Duration
	SpoolDir     string // local scratch space for COLD exports
	TestHook     string // fault to inject for failure tests; empty in normal use
	DuckDBMemory string // memory limit of each DuckDB database, e.g. "256MB"
}

type Service struct {
	cfg    Config
	log    *slog.Logger
	db     *pgxpool.Pool
	cat    *catalog.Catalog
	search *search.Client
	store  *objectstore.Store
	duck   *sql.DB // in-memory DuckDB that can read the object store
	active atomic.Bool
}

func New(cfg Config, log *slog.Logger, db *pgxpool.Pool, sc *search.Client, store *objectstore.Store, duck *sql.DB) *Service {
	return &Service{cfg: cfg, log: log, db: db, cat: catalog.New(db), search: sc, store: store, duck: duck}
}

// Ready fails while this instance is a standby waiting for the lock.
func (s *Service) Ready(context.Context) error {
	if !s.active.Load() {
		return errors.New("waiting for the dlm lock")
	}
	return nil
}

// Run takes the lock, then plans and runs jobs until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	conn, err := s.lock(ctx)
	if err != nil {
		return nil // cancelled while waiting
	}
	defer func() {
		s.active.Store(false)
		lockHeld.Set(0)
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Exec(bg, `SELECT pg_advisory_unlock(hashtext('obs.dlm'))`)
		conn.Release()
	}()

	plan := time.NewTicker(s.cfg.PlanInterval)
	defer plan.Stop()
	run := time.NewTicker(s.cfg.RunInterval)
	defer run.Stop()

	s.plan(ctx)
	s.runJobs(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-plan.C:
			s.plan(ctx)
		case <-run.C:
			s.runJobs(ctx)
		}
	}
}

// lock takes a session advisory lock so only one dlm works at a time
// (ADR-0005). The lock lives as long as the returned connection.
func (s *Service) lock(ctx context.Context) (*pgxpool.Conn, error) {
	for {
		conn, err := s.db.Acquire(ctx)
		if err == nil {
			var ok bool
			err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('obs.dlm'))`).Scan(&ok)
			if err == nil && ok {
				s.active.Store(true)
				lockHeld.Set(1)
				s.log.Info("dlm lock acquired")
				return conn, nil
			}
			conn.Release()
		}
		if err != nil {
			s.log.Warn("take dlm lock", "err", err)
		} else {
			s.log.Info("another dlm holds the lock, waiting")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockRetry):
		}
	}
}

// plan seals partitions that stopped receiving data and creates jobs for
// partitions that are old enough to move.
func (s *Service) plan(ctx context.Context) {
	parts, err := s.cat.SealCandidates(ctx)
	if err != nil {
		s.log.Error("find partitions to seal", "err", err)
		return
	}
	for _, p := range parts {
		if err := s.seal(ctx, p); err != nil {
			s.log.Error("seal partition", "partition_id", p.ID, "err", err)
		}
	}

	s.refreshOpenStats(ctx)
	s.rollup(ctx)

	n, err := s.cat.PlanJobs(ctx)
	if err != nil {
		s.log.Error("plan jobs", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("jobs created", "count", n)
	}
}

// refreshOpenStats keeps the size of partitions that are still being written
// up to date in the catalog. Failures only affect the dashboards.
func (s *Service) refreshOpenStats(ctx context.Context) {
	parts, err := s.cat.OpenPartitions(ctx)
	if err != nil {
		s.log.Warn("list open partitions", "err", err)
		return
	}
	for _, p := range parts {
		docs, err := s.search.Count(ctx, p.Location)
		if err != nil {
			continue // the index may not exist yet
		}
		size, err := s.search.StoreBytes(ctx, p.Location)
		if err != nil {
			continue
		}
		if err := s.cat.UpdateOpenStats(ctx, p.ID, docs, size); err != nil {
			s.log.Warn("update open partition stats", "partition_id", p.ID, "err", err)
		}
	}
}

// seal blocks writes before counting, so doc_count stays correct for the
// life of the partition.
func (s *Service) seal(ctx context.Context, p catalog.Partition) error {
	if err := s.search.BlockWrites(ctx, p.Location); err != nil {
		return err
	}
	if err := s.search.Refresh(ctx, p.Location); err != nil {
		return err
	}
	docs, err := s.search.Count(ctx, p.Location)
	if err != nil {
		return err
	}
	size, err := s.search.StoreBytes(ctx, p.Location)
	if err != nil {
		return err
	}
	if err := s.cat.SealPartition(ctx, p.ID, docs, size); err != nil {
		return err
	}
	s.log.Info("partition sealed", "partition_id", p.ID, "index", p.Location, "docs", docs, "bytes", size)
	return nil
}
