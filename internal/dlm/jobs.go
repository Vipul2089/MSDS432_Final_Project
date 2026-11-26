package dlm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"obsfabric/internal/catalog"
	"obsfabric/internal/cold"
)

const mergeTimeout = 10 * time.Minute

var (
	jobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dlm_jobs_total",
		Help: "Finished migration jobs, by type and result (done or failed).",
	}, []string{"type", "result"})
	stepSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dlm_step_duration_seconds",
		Help:    "Duration of each job step.",
		Buckets: []float64{.1, .5, 1, 5, 15, 60, 300, 900},
	}, []string{"type", "step"})
)

// runJobs runs due jobs one step at a time until none is left. Each step
// saves the next status before the following step starts.
func (s *Service) runJobs(ctx context.Context) {
	for ctx.Err() == nil {
		job, ok, err := s.cat.NextJob(ctx)
		if err != nil {
			s.log.Error("load next job", "err", err)
			return
		}
		if !ok {
			return
		}
		s.step(ctx, job)
	}
}

func (s *Service) step(ctx context.Context, job catalog.Job) {
	start := time.Now()
	var err error
	switch job.Type {
	case "HOT_TO_WARM":
		err = s.hotToWarm(ctx, job)
	case "WARM_TO_COLD":
		err = s.warmToCold(ctx, job)
	case "DELETE":
		err = s.deletePartition(ctx, job)
	default:
		err = fmt.Errorf("job type %s is not supported", job.Type)
	}
	stepSeconds.WithLabelValues(job.Type, job.Status).Observe(time.Since(start).Seconds())
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		return // shutting down; the step runs again after restart
	}

	next, delay := afterError(job, err)
	s.log.Error("job step failed", "job_id", job.ID, "partition_id", job.PartitionID,
		"step", job.Status, "next", next, "err", err)
	if err := s.cat.MoveJob(ctx, job.ID, next, err.Error(), time.Now().Add(delay)); err != nil {
		s.log.Error("save job status", "job_id", job.ID, "err", err)
		return
	}
	if next == "FAILED" {
		jobsTotal.WithLabelValues(job.Type, "failed").Inc()
	}
}

// afterError decides where a job goes after a failed step (LLD-001 §6.3.4).
// A failed copy or check is rolled back; other steps are retried later.
func afterError(job catalog.Job, err error) (status string, delay time.Duration) {
	switch {
	case errors.Is(err, catalog.ErrVersionConflict):
		return "FAILED", 0
	case job.Status == "COPYING" || job.Status == "VERIFYING":
		return "ROLLING_BACK", 0
	default:
		return job.Status, backoff(job.Attempt)
	}
}

// afterRollback retries the job from the start, or gives up after
// max_attempts.
func afterRollback(job catalog.Job) (status string, delay time.Duration) {
	if job.Attempt >= job.MaxAttempts {
		return "FAILED", 0
	}
	return "PENDING", backoff(job.Attempt)
}

// backoff is 2^attempt minutes.
func backoff(attempt int) time.Duration {
	return time.Minute << max(attempt, 0)
}

func (s *Service) hotToWarm(ctx context.Context, job catalog.Job) error {
	index := job.Location
	switch job.Status {
	case "PENDING":
		return s.cat.StartAttempt(ctx, job.ID, "COPYING", "")

	case "COPYING":
		ctx, cancel := context.WithTimeout(ctx, mergeTimeout)
		defer cancel()
		if err := s.search.ForceMerge(ctx, index); err != nil {
			return fmt.Errorf("force-merge: %w", err)
		}
		// Release the merged-away segments so the size recorded at the
		// switch is the real one.
		if err := s.search.Refresh(ctx, index); err != nil {
			return err
		}
		if err := s.search.SetReplicas(ctx, index, 0); err != nil {
			return err
		}
		if err := s.search.WaitGreen(ctx, index, mergeTimeout); err != nil {
			return err
		}
		return s.cat.MoveJob(ctx, job.ID, "VERIFYING", "", time.Now())

	case "VERIFYING":
		n, err := s.search.Count(ctx, index)
		if err != nil {
			return err
		}
		if err := s.cat.SetJobSource(ctx, job.ID, job.DocCount, ""); err != nil {
			return err
		}
		if err := s.cat.SetJobTarget(ctx, job.ID, n, ""); err != nil {
			return err
		}
		if n != job.DocCount {
			return fmt.Errorf("index %s has %d documents, catalog expects %d", index, n, job.DocCount)
		}
		return s.cat.MoveJob(ctx, job.ID, "SWITCHING", "", time.Now())

	case "SWITCHING":
		size, err := s.search.StoreBytes(ctx, index)
		if err != nil {
			return err
		}
		if err := s.cat.SwitchTier(ctx, job, "HOT", "WARM", size); err != nil {
			return err
		}
		jobsTotal.WithLabelValues(job.Type, "done").Inc()
		s.log.Info("partition moved to WARM", "job_id", job.ID, "partition_id", job.PartitionID, "index", index, "bytes", size)
		return nil

	case "ROLLING_BACK":
		// Force-merge and replica changes leave a valid HOT index, so there is
		// nothing to undo on a single node.
		next, delay := afterRollback(job)
		return s.cat.MoveJob(ctx, job.ID, next, "", time.Now().Add(delay))
	}
	return fmt.Errorf("unexpected status %s", job.Status)
}

// deletePartition removes a partition older than retain_for. The router stops
// reading it at the switch; the data itself goes after purge_grace.
func (s *Service) deletePartition(ctx context.Context, job catalog.Job) error {
	switch job.Status {
	case "PENDING":
		return s.cat.StartAttempt(ctx, job.ID, "SWITCHING", "")

	case "SWITCHING":
		if err := s.cat.MarkDeleted(ctx, job); err != nil {
			return err
		}
		s.log.Info("partition deleted from the catalog", "job_id", job.ID, "partition_id", job.PartitionID, "tier", job.Tier)
		return nil

	case "PURGE_PENDING":
		var err error
		if job.Tier == "COLD" {
			err = s.store.DeletePrefix(ctx, cold.BucketPrefix(job.Start))
		} else {
			err = s.search.DeleteIndex(ctx, job.Location)
		}
		if err != nil {
			return err
		}
		if err := s.cat.FinishJob(ctx, job.ID); err != nil {
			return err
		}
		jobsTotal.WithLabelValues(job.Type, "done").Inc()
		s.log.Info("partition data purged", "job_id", job.ID, "partition_id", job.PartitionID, "tier", job.Tier)
		return nil
	}
	return fmt.Errorf("unexpected status %s", job.Status)
}
