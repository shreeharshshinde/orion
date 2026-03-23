package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
	"github.com/shreeharshshinde/orion/internal/store"
)

const (
	// advisoryLockKey is the PostgreSQL advisory lock key for scheduler leader election.
	// Only one scheduler instance holds this lock at a time.
	advisoryLockKey = 7331001 // arbitrary stable integer; document this value

	defaultBatchSize        = 50
	defaultScheduleInterval = 2 * time.Second
	defaultOrphanInterval   = 30 * time.Second
	workerHeartbeatTTL      = 45 * time.Second
)

// Config holds scheduler configuration.
type Config struct {
	BatchSize        int
	ScheduleInterval time.Duration
	OrphanInterval   time.Duration
}

// Scheduler is responsible for:
//  1. Acquiring leader election lock (only one scheduler runs at a time)
//  2. Polling for queued jobs and dispatching them to the queue broker
//  3. Sweeping orphaned/stale jobs and requeuing them
//  4. Promoting failed-but-retryable jobs when their next_retry_at has passed
type Scheduler struct {
	cfg    Config
	db     *pgxpool.Pool // direct DB access for advisory locks
	store  store.Store
	queue  queue.Queue
	logger *slog.Logger
}

// New creates a Scheduler. The db pool is needed for advisory lock management.
func New(cfg Config, db *pgxpool.Pool, s store.Store, q queue.Queue, logger *slog.Logger) *Scheduler {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.ScheduleInterval == 0 {
		cfg.ScheduleInterval = defaultScheduleInterval
	}
	if cfg.OrphanInterval == 0 {
		cfg.OrphanInterval = defaultOrphanInterval
	}
	return &Scheduler{cfg: cfg, db: db, store: s, queue: q, logger: logger}
}

// Run starts the scheduler. It blocks until ctx is cancelled.
// It will repeatedly attempt to acquire leader election, and only
// schedule when it holds the lock. On lock loss, it backs off and retries.
func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Info("scheduler starting, contending for leader lock")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		held, err := s.tryAcquireLeaderLock(ctx)
		if err != nil {
			s.logger.Error("error acquiring leader lock", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				continue
			}
		}

		if !held {
			// Another instance holds the lock — wait and retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
				continue
			}
		}

		s.logger.Info("acquired scheduler leader lock")
		s.runAsLeader(ctx)
		s.logger.Warn("lost scheduler leader lock, re-contending")
	}
}

// tryAcquireLeaderLock attempts to acquire a PostgreSQL session advisory lock.
// pg_try_advisory_lock returns true if the lock was acquired, false otherwise.
// The lock is automatically released when the DB connection is closed.
func (s *Scheduler) tryAcquireLeaderLock(ctx context.Context) (bool, error) {
	var held bool
	err := s.db.QueryRow(ctx,
		"SELECT pg_try_advisory_lock($1)", advisoryLockKey,
	).Scan(&held)
	return held, err
}

// releaseLeaderLock explicitly releases the advisory lock.
func (s *Scheduler) releaseLeaderLock(ctx context.Context) {
	_, err := s.db.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	if err != nil {
		s.logger.Error("failed to release leader lock", "err", err)
	}
}

// runAsLeader runs the scheduling loops while this instance holds the leader lock.
// Returns when the lock is lost or ctx is cancelled.
func (s *Scheduler) runAsLeader(ctx context.Context) {
	defer s.releaseLeaderLock(ctx)

	scheduleTicker := time.NewTicker(s.cfg.ScheduleInterval)
	orphanTicker := time.NewTicker(s.cfg.OrphanInterval)
	defer scheduleTicker.Stop()
	defer orphanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-scheduleTicker.C:
			if err := s.scheduleQueuedJobs(ctx); err != nil {
				s.logger.Error("schedule loop error", "err", err)
			}
			if err := s.promoteRetryableJobs(ctx); err != nil {
				s.logger.Error("retry promotion error", "err", err)
			}

		case <-orphanTicker.C:
			if err := s.reclaimOrphanedJobs(ctx); err != nil {
				s.logger.Error("orphan reclaim error", "err", err)
			}
		}
	}
}

// scheduleQueuedJobs reads queued jobs from PostgreSQL and pushes them
// into the Redis queue. Jobs are ordered by priority DESC, created_at ASC.
func (s *Scheduler) scheduleQueuedJobs(ctx context.Context) error {
	status := domain.JobStatusQueued
	jobs, err := s.store.ListJobs(ctx, store.JobFilter{
		Status: &status,
		Limit:  s.cfg.BatchSize,
	})
	if err != nil {
		return fmt.Errorf("listing queued jobs: %w", err)
	}

	dispatched := 0
	for _, job := range jobs {
		// Transition to scheduled in PG before pushing to queue.
		// If this fails (concurrent scheduler picked it up), skip.
		err := s.store.TransitionJobState(ctx,
			job.ID,
			domain.JobStatusQueued,
			domain.JobStatusScheduled,
		)
		if err != nil {
			// State conflict is expected under concurrent schedulers — not an error
			s.logger.Debug("state conflict scheduling job", "job_id", job.ID, "err", err)
			continue
		}

		if err := s.queue.Enqueue(ctx, job); err != nil {
			// Roll back the transition if queue push fails
			_ = s.store.TransitionJobState(ctx,
				job.ID,
				domain.JobStatusScheduled,
				domain.JobStatusQueued,
			)
			s.logger.Error("failed to enqueue job", "job_id", job.ID, "err", err)
			continue
		}
		dispatched++
	}

	if dispatched > 0 {
		s.logger.Info("dispatched jobs to queue", "count", dispatched)
	}
	return nil
}

// promoteRetryableJobs finds failed jobs whose next_retry_at has passed
// and requeues them.
func (s *Scheduler) promoteRetryableJobs(ctx context.Context) error {
	// In the postgres implementation, this query is:
	// SELECT * FROM jobs WHERE status = 'failed' AND attempt < max_retries
	//   AND next_retry_at <= NOW() LIMIT N
	// For now we reuse the filter mechanism; the postgres impl handles the time filter.
	status := domain.JobStatusFailed
	jobs, err := s.store.ListJobs(ctx, store.JobFilter{
		Status: &status,
		Limit:  s.cfg.BatchSize,
	})
	if err != nil {
		return fmt.Errorf("listing retryable jobs: %w", err)
	}

	for _, job := range jobs {
		if !job.IsRetryable() {
			continue
		}
		if job.NextRetryAt != nil && time.Now().Before(*job.NextRetryAt) {
			continue
		}

		// failed → retrying → queued (two-step to preserve audit clarity)
		if err := s.store.TransitionJobState(ctx, job.ID,
			domain.JobStatusFailed, domain.JobStatusRetrying); err != nil {
			continue
		}
		if err := s.store.TransitionJobState(ctx, job.ID,
			domain.JobStatusRetrying, domain.JobStatusQueued); err != nil {
			continue
		}
		s.logger.Info("promoted job for retry", "job_id", job.ID, "attempt", job.Attempt)
	}
	return nil
}

// reclaimOrphanedJobs finds jobs stuck in 'running' whose worker has stopped heartbeating.
func (s *Scheduler) reclaimOrphanedJobs(ctx context.Context) error {
	count, err := s.store.ReclaimOrphanedJobs(ctx, workerHeartbeatTTL*2)
	if err != nil {
		return fmt.Errorf("reclaiming orphaned jobs: %w", err)
	}
	if count > 0 {
		s.logger.Warn("reclaimed orphaned jobs", "count", count)
	}
	return nil
}