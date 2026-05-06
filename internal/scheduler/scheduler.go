package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/observability"
	"github.com/shreeharshshinde/orion/internal/pipeline"
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
//  5. [Phase 5] Advancing all active pipelines on every schedule tick
//
// Phase 6: metrics field added — all metric calls are nil-guarded.
// Phase 8: rateLimiter and queueAllocations added for fair scheduling.
type Scheduler struct {
	cfg              Config
	db               *pgxpool.Pool // direct DB access for advisory locks
	store            store.Store
	queue            queue.Queue
	advancer         *pipeline.Advancer      // Phase 5: DAG advancement engine
	metrics          *observability.Metrics  // Phase 6
	rateLimiter      *QueueRateLimiter       // Phase 8: token bucket per queue
	queueAllocations map[string]QueueAllocation // Phase 8: weighted dispatch config
	logger           *slog.Logger
}

// New creates a Scheduler. The db pool is needed for advisory lock management.
// advancer is the Phase 5 pipeline advancement engine — pass pipeline.NewAdvancer(store, logger).
// Phase 6 change: accepts *observability.Metrics as the sixth argument.
// Phase 8 change: accepts *QueueRateLimiter and map[string]QueueAllocation.
// m, rateLimiter, and queueAllocations may be nil; all calls are nil-guarded.
func New(
	cfg Config,
	db *pgxpool.Pool,
	s store.Store,
	q queue.Queue,
	adv *pipeline.Advancer,
	m *observability.Metrics,
	rateLimiter *QueueRateLimiter,
	queueAllocations map[string]QueueAllocation,
	logger *slog.Logger,
) *Scheduler {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.ScheduleInterval == 0 {
		cfg.ScheduleInterval = defaultScheduleInterval
	}
	if cfg.OrphanInterval == 0 {
		cfg.OrphanInterval = defaultOrphanInterval
	}
	return &Scheduler{
		cfg:              cfg,
		db:               db,
		store:            s,
		queue:            q,
		advancer:         adv,
		metrics:          m,
		rateLimiter:      rateLimiter,
		queueAllocations: queueAllocations,
		logger:           logger,
	}
}

// Run blocks until ctx is cancelled, contending for leader election and
// dispatching jobs when this instance holds the advisory lock.
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
//
// Three loops:
//   - scheduleTicker (every 2s): dispatch queued jobs + promote retries + advance pipelines
//   - orphanTicker   (every 30s): reclaim jobs whose worker went silent
// Phase 6 change: scheduleQueuedJobs now measures cycle latency and emits spans.
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
			// ── 1. Dispatch queued jobs → Redis ──────────────────────────
			if err := s.scheduleQueuedJobs(ctx); err != nil {
				s.logger.Error("schedule loop error", "err", err)
			}

			// ── 2. Promote failed → retrying → queued (backoff passed) ──
			if err := s.promoteRetryableJobs(ctx); err != nil {
				s.logger.Error("retry promotion error", "err", err)
			}

			// ── 3. Advance all active pipelines [Phase 5] ───────────────
			// advancePipelines runs on the same 2-second tick as job
			// dispatch. This gives a maximum 2-second latency between
			// a node completing and the next node starting.
			// The leader lock guarantees only one scheduler runs this.
			if s.advancer != nil {
				if err := s.advancer.AdvanceAll(ctx); err != nil {
					s.logger.Error("pipeline advancement error", "err", err)
				}
			}

		case <-orphanTicker.C:
			// ── 4. Reclaim jobs whose worker went silent ─────────────────
			if err := s.reclaimOrphanedJobs(ctx); err != nil {
				s.logger.Error("orphan reclaim error", "err", err)
			}
		}
	}
}

// scheduleQueuedJobs reads queued jobs from PostgreSQL and pushes them into Redis.
//
// Phase 8 change: replaces the single ListJobs call with FairQueue.FetchReadyJobs,
// which issues one query per queue with a weight-proportional limit. This prevents
// any single queue from monopolising the dispatch batch.
//
// Dispatch order within the batch: highest-weight queue first.
// Rate limiter: each job is checked against the token bucket before dispatch.
// Skipped jobs remain in 'queued' status and are picked up on the next tick.
func (s *Scheduler) scheduleQueuedJobs(ctx context.Context) error {
	ctx, span := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_cycle")
	defer span.End()

	cycleStart := time.Now()
	defer func() {
		elapsed := time.Since(cycleStart).Seconds()
		if s.metrics != nil {
			s.metrics.SchedulerCycleLatency.Observe(elapsed)
		}
		span.SetAttributes(attribute.Float64("cycle.elapsed_seconds", elapsed))
	}()

	// ── Phase 8: weighted fair allocation ────────────────────────────────────
	// Fall back to a single unfiltered query when no allocations are configured
	// (e.g. in tests that don't wire Phase 8 components).
	var jobs []*domain.Job
	var err error

	if len(s.queueAllocations) > 0 && s.rateLimiter != nil {
		allocations := ComputeAllocations(s.queueAllocations, s.cfg.BatchSize)
		fairQ := NewFairQueue(allocations, s.rateLimiter, s.store, s.logger)
		jobs, err = fairQ.FetchReadyJobs(ctx)
	} else {
		statusQueued := domain.JobStatusQueued
		jobs, err = s.store.ListJobs(ctx, store.JobFilter{
			Status: &statusQueued,
			Limit:  s.cfg.BatchSize,
		})
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("fetching ready jobs: %w", err)
	}

	span.SetAttributes(attribute.Int("jobs.found", len(jobs)))

	dispatched := 0
	rateLimited := 0

	for _, job := range jobs {
		// ── Rate limiter check ────────────────────────────────────────────────
		if s.rateLimiter != nil && !s.rateLimiter.Allow(job.QueueName) {
			rateLimited++
			if s.metrics != nil {
				s.metrics.QueueRateLimited.WithLabelValues(job.QueueName).Inc()
			}
			continue // job stays queued; picked up next tick when tokens refill
		}

		_, jobSpan := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_job")
		jobSpan.SetAttributes(
			attribute.String("job.id", job.ID.String()),
			attribute.String("job.type", string(job.Type)),
			attribute.String("job.queue", job.QueueName),
			attribute.Int("job.priority", int(job.Priority)),
		)

		if err := s.store.TransitionJobState(ctx,
			job.ID, domain.JobStatusQueued, domain.JobStatusScheduled,
		); err != nil {
			s.logger.Debug("state conflict scheduling job", "job_id", job.ID)
			jobSpan.End()
			continue
		}

		if err := s.queue.Enqueue(ctx, job); err != nil {
			_ = s.store.TransitionJobState(ctx,
				job.ID, domain.JobStatusScheduled, domain.JobStatusQueued,
			)
			s.logger.Error("failed to enqueue job", "job_id", job.ID, "err", err)
			jobSpan.SetStatus(codes.Error, err.Error())
			jobSpan.End()
			continue
		}

		if s.metrics != nil {
			s.metrics.JobsSubmitted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
		}
		jobSpan.End()
		dispatched++
	}

	span.SetAttributes(
		attribute.Int("jobs.dispatched", dispatched),
		attribute.Int("jobs.rate_limited", rateLimited),
	)
	if dispatched > 0 || rateLimited > 0 {
		s.logger.Info("scheduler cycle complete",
			"dispatched", dispatched, "rate_limited", rateLimited)
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
