package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
	"github.com/shreeharshshinde/orion/internal/store"
	"github.com/shreeharshshinde/orion/pkg/retry"
)

// Executor is the interface that performs actual job work.
// Implementations:
//   - InlineExecutor  (Phase 3) — runs registered Go functions
//   - KubernetesExecutor (Phase 4) — launches and waits for K8s Jobs
type Executor interface {
	Execute(ctx context.Context, job *domain.Job) error
	// CanExecute returns true if this executor handles the given job type.
	// The pool calls this to route each job to the correct executor.
	CanExecute(jobType domain.JobType) bool
}

// WorkerConfig holds all tunable configuration for the worker pool.
type WorkerConfig struct {
	WorkerID          string
	QueueNames        []string
	Concurrency       int
	VisibilityTimeout time.Duration
	HeartbeatInterval time.Duration
	ShutdownTimeout   time.Duration
}

// Pool is a bounded goroutine pool that dequeues jobs from Redis and executes them.
//
// Architecture:
//   - One dequeue goroutine per queue name (pulls from Redis Streams)
//   - N worker goroutines (N = Concurrency, default 10)
//   - One buffered jobCh channel connecting them (capacity = Concurrency)
//
// The buffered channel provides backpressure: when all workers are busy,
// the dequeue goroutines block on send. Jobs stay in Redis (not in memory)
// until a worker slot is available. Crash-safe: no in-flight jobs are lost.
type Pool struct {
	cfg       WorkerConfig
	queue     queue.Queue
	store     store.Store
	executors []Executor
	logger    *slog.Logger

	// jobCh is the backpressure boundary between dequeue goroutines and workers.
	// Capacity = cfg.Concurrency — when full, dequeue goroutines block.
	jobCh chan *jobTask

	activeCount atomic.Int32
	wg          sync.WaitGroup
}

type jobTask struct {
	job   *domain.Job
	ackFn queue.AckFunc
}

// NewPool creates a Pool. Call Start() to begin processing jobs.
func NewPool(cfg WorkerConfig, q queue.Queue, s store.Store, executors []Executor, logger *slog.Logger) *Pool {
	return &Pool{
		cfg:       cfg,
		queue:     q,
		store:     s,
		executors: executors,
		logger:    logger,
		jobCh:     make(chan *jobTask, cfg.Concurrency),
	}
}

// Start launches the worker goroutines and the dequeue loop.
// Blocks until ctx is cancelled. On return, all in-flight jobs have completed
// or ShutdownTimeout has elapsed.
func (p *Pool) Start(ctx context.Context) error {
	p.logger.Info("starting worker pool",
		"worker_id", p.cfg.WorkerID,
		"concurrency", p.cfg.Concurrency,
		"queues", p.cfg.QueueNames,
	)

	if err := p.store.RegisterWorker(ctx, &domain.Worker{
		ID:           p.cfg.WorkerID,
		QueueNames:   p.cfg.QueueNames,
		Concurrency:  p.cfg.Concurrency,
		Status:       domain.WorkerStatusIdle,
		RegisteredAt: time.Now(),
	}); err != nil {
		return fmt.Errorf("registering worker: %w", err)
	}

	// Launch N worker goroutines.
	for i := 0; i < p.cfg.Concurrency; i++ {
		p.wg.Add(1)
		go p.runWorker(ctx, i)
	}

	// Send heartbeats in the background.
	go p.heartbeatLoop(ctx)

	// Run the dequeue loop in the calling goroutine (blocks until ctx cancelled).
	p.dequeueLoop(ctx)

	// ctx cancelled: drain remaining in-flight jobs.
	return p.drain()
}

// dequeueLoop pulls jobs from every configured queue and dispatches them to jobCh.
// Sending to jobCh blocks when all worker slots are occupied — natural backpressure.
func (p *Pool) dequeueLoop(ctx context.Context) {
	for _, queueName := range p.cfg.QueueNames {
		go func(qName string) {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				job, ackFn, err := p.queue.Dequeue(ctx, qName, p.cfg.VisibilityTimeout)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					p.logger.Error("dequeue error", "queue", qName, "err", err)
					time.Sleep(time.Second)
					continue
				}
				if job == nil {
					continue
				}

				// This send blocks when jobCh is at capacity.
				// Jobs stay in Redis PEL until a slot opens — never lost.
				select {
				case <-ctx.Done():
					_ = ackFn(fmt.Errorf("worker shutting down"))
					return
				case p.jobCh <- &jobTask{job: job, ackFn: ackFn}:
				}
			}
		}(queueName)
	}
}

// runWorker is a single worker goroutine. Reads tasks from jobCh and executes them.
func (p *Pool) runWorker(ctx context.Context, id int) {
	defer p.wg.Done()
	logger := p.logger.With("worker_slot", id)

	for {
		select {
		case task, ok := <-p.jobCh:
			if !ok {
				return
			}
			p.activeCount.Add(1)
			p.executeJob(ctx, task, logger)
			p.activeCount.Add(-1)

		case <-ctx.Done():
			// Drain any tasks already in the channel before exiting.
			for {
				select {
				case task, ok := <-p.jobCh:
					if !ok {
						return
					}
					p.activeCount.Add(1)
					p.executeJob(context.Background(), task, logger)
					p.activeCount.Add(-1)
				default:
					return
				}
			}
		}
	}
}

// executeJob runs a single job through the appropriate executor.
// It owns all state transitions (MarkJobRunning, MarkJobCompleted, MarkJobFailed)
// and writes the immutable execution audit record (RecordExecution).
//
// Phase 3 addition: RecordExecution calls capture each attempt in job_executions.
func (p *Pool) executeJob(ctx context.Context, task *jobTask, logger *slog.Logger) {
	job := task.job
	logger = logger.With("job_id", job.ID, "job_name", job.Name, "attempt", job.Attempt)
	startedAt := time.Now()

	// ── Transition: scheduled → running ─────────────────────────────────────
	// CAS in PostgreSQL: only succeeds if status is still 'scheduled'.
	// If another worker raced us here (shouldn't happen), ErrStateConflict fires
	// and we skip this job (NACK so Redis re-delivers it).
	if err := p.store.MarkJobRunning(ctx, job.ID, p.cfg.WorkerID); err != nil {
		logger.Error("failed to mark job running", "err", err)
		_ = task.ackFn(err)
		return
	}

	// ── Record execution start in audit log ──────────────────────────────────
	// Write the first audit row immediately when the job starts running.
	// This ensures even jobs that crash mid-execution have an audit record.
	// ON CONFLICT DO NOTHING makes this idempotent on retry.
	_ = p.store.RecordExecution(ctx, &domain.JobExecution{
		ID:        uuid.New(),
		JobID:     job.ID,
		Attempt:   job.Attempt + 1, // DB attempt is 0-indexed before any failure; audit is 1-indexed
		WorkerID:  p.cfg.WorkerID,
		Status:    domain.JobStatusRunning,
		StartedAt: &startedAt,
	})

	// ── Resolve executor ─────────────────────────────────────────────────────
	// Phase 2: always nil → MarkJobFailed("no executor")
	// Phase 3: InlineExecutor.CanExecute("inline") = true → Execute()
	// Phase 4: KubernetesExecutor added for "k8s_job"
	executor := p.resolveExecutor(job.Type)
	if executor == nil {
		msg := fmt.Sprintf("no executor registered for job type %q", job.Type)
		logger.Error(msg)
		finishedAt := time.Now()
		_ = p.store.MarkJobFailed(ctx, job.ID, msg, p.nextRetryTime(job))
		_ = p.store.RecordExecution(ctx, &domain.JobExecution{
			ID:         uuid.New(),
			JobID:      job.ID,
			Attempt:    job.Attempt + 1,
			WorkerID:   p.cfg.WorkerID,
			Status:     domain.JobStatusFailed,
			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			Error:      msg,
		})
		_ = task.ackFn(fmt.Errorf(msg))
		return
	}

	logger.Info("executing job", "type", job.Type)

	// ── Apply per-job deadline if the caller specified one ───────────────────
	execCtx := ctx
	if job.Deadline != nil {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithDeadline(ctx, *job.Deadline)
		defer cancel()
	}

	// ── Execute ──────────────────────────────────────────────────────────────
	err := executor.Execute(execCtx, job)
	finishedAt := time.Now()

	// ── Handle result ────────────────────────────────────────────────────────
	if err != nil {
		logger.Warn("job execution failed",
			"err", err,
			"elapsed_ms", finishedAt.Sub(startedAt).Milliseconds(),
		)
		retryAt := p.nextRetryTime(job)
		_ = p.store.MarkJobFailed(ctx, job.ID, err.Error(), retryAt)
		_ = p.store.RecordExecution(ctx, &domain.JobExecution{
			ID:         uuid.New(),
			JobID:      job.ID,
			Attempt:    job.Attempt + 1,
			WorkerID:   p.cfg.WorkerID,
			Status:     domain.JobStatusFailed,
			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			Error:      err.Error(),
		})
		_ = task.ackFn(err) // NACK: message stays in Redis PEL for redelivery
		return
	}

	logger.Info("job completed successfully",
		"elapsed_ms", finishedAt.Sub(startedAt).Milliseconds(),
	)
	_ = p.store.MarkJobCompleted(ctx, job.ID)
	_ = p.store.RecordExecution(ctx, &domain.JobExecution{
		ID:         uuid.New(),
		JobID:      job.ID,
		Attempt:    job.Attempt + 1,
		WorkerID:   p.cfg.WorkerID,
		Status:     domain.JobStatusCompleted,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
	})
	_ = task.ackFn(nil) // ACK: removes message from Redis PEL permanently ✓
}

// resolveExecutor returns the first executor that handles the given job type,
// or nil if no executor is registered for it.
func (p *Pool) resolveExecutor(jobType domain.JobType) Executor {
	for _, e := range p.executors {
		if e.CanExecute(jobType) {
			return e
		}
	}
	return nil
}

// nextRetryTime computes the next retry timestamp using full-jitter backoff.
// Returns nil if the job has exhausted its max_retries.
func (p *Pool) nextRetryTime(job *domain.Job) *time.Time {
	if !job.IsRetryable() {
		return nil
	}
	delay := retry.FullJitterBackoff(job.Attempt, 5*time.Second, 30*time.Minute)
	t := time.Now().Add(delay)
	return &t
}

// drain closes jobCh and waits for all in-flight jobs to finish.
// Returns an error if jobs do not complete within ShutdownTimeout.
func (p *Pool) drain() error {
	p.logger.Info("draining worker pool")
	close(p.jobCh)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Info("worker pool drained cleanly")
		return nil
	case <-time.After(p.cfg.ShutdownTimeout):
		p.logger.Warn("worker pool drain timed out", "timeout", p.cfg.ShutdownTimeout)
		return fmt.Errorf("shutdown timeout exceeded after %s", p.cfg.ShutdownTimeout)
	}
}

// heartbeatLoop periodically updates last_heartbeat in PostgreSQL so the
// scheduler can detect alive workers. Deregisters the worker on clean exit.
func (p *Pool) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = p.store.DeregisterWorker(context.Background(), p.cfg.WorkerID)
			return
		case <-ticker.C:
			if err := p.store.Heartbeat(ctx, p.cfg.WorkerID); err != nil {
				p.logger.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

// ActiveCount returns the number of jobs currently being executed.
// Used by metrics and health checks.
func (p *Pool) ActiveCount() int {
	return int(p.activeCount.Load())
}
