package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
	"github.com/shreeharshshinde/orion/internal/store"
	"github.com/shreeharshshinde/orion/pkg/retry"
)

// Executor is the interface that performs actual job work.
// Implementations: InlineExecutor (runs registered handlers),
// KubernetesExecutor (launches K8s Jobs).
type Executor interface {
	Execute(ctx context.Context, job *domain.Job) error
	// CanExecute returns true if this executor handles the given job type.
	CanExecute(jobType domain.JobType) bool
}

// WorkerConfig holds all configuration for the worker pool.
type WorkerConfig struct {
	WorkerID          string
	QueueNames        []string
	Concurrency       int
	VisibilityTimeout time.Duration
	HeartbeatInterval time.Duration
	ShutdownTimeout   time.Duration
}

// Pool is a bounded worker pool that dequeues and executes jobs concurrently.
// It respects backpressure: the job channel has capacity = Concurrency,
// so the dequeue loop blocks naturally when all workers are busy.
type Pool struct {
	cfg       WorkerConfig
	queue     queue.Queue
	store     store.Store
	executors []Executor
	logger    *slog.Logger

	// jobCh is the internal dispatch channel.
	// Capacity = cfg.Concurrency provides natural backpressure.
	jobCh chan *jobTask

	activeCount atomic.Int32
	wg          sync.WaitGroup
}

type jobTask struct {
	job   *domain.Job
	ackFn queue.AckFunc
}

// NewPool creates a worker pool. Call Start to begin processing.
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
// or the ShutdownTimeout has elapsed.
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

	// Start N worker goroutines
	for i := 0; i < p.cfg.Concurrency; i++ {
		p.wg.Add(1)
		go p.runWorker(ctx, i)
	}

	// Background: send heartbeats every HeartbeatInterval
	go p.heartbeatLoop(ctx)

	// Dequeue loop — runs in the calling goroutine, blocks until ctx cancelled
	p.dequeueLoop(ctx)

	// ctx cancelled: begin graceful drain
	return p.drain()
}

// dequeueLoop continuously pulls jobs from the queue and dispatches them
// to the jobCh. Sending to jobCh blocks when all workers are busy,
// which is the backpressure mechanism — jobs stay in Redis, not in memory.
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
					time.Sleep(time.Second) // brief backoff on transient errors
					continue
				}
				if job == nil {
					continue
				}

				// This send BLOCKS if all worker slots are occupied.
				// That is intentional backpressure — we stop pulling from Redis
				// until a worker slot frees up.
				select {
				case <-ctx.Done():
					// Can't process it — NACK so Redis redelivers it
					_ = ackFn(fmt.Errorf("worker shutting down"))
					return
				case p.jobCh <- &jobTask{job: job, ackFn: ackFn}:
				}
			}
		}(queueName)
	}
}

// runWorker is a single worker goroutine. It reads from jobCh and executes jobs.
func (p *Pool) runWorker(ctx context.Context, id int) {
	defer p.wg.Done()
	logger := p.logger.With("worker_slot", id)

	for {
		select {
		case task, ok := <-p.jobCh:
			if !ok {
				return // channel closed during drain
			}
			p.activeCount.Add(1)
			p.executeJob(ctx, task, logger)
			p.activeCount.Add(-1)

		case <-ctx.Done():
			// Drain any remaining tasks already in the channel before exiting
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
// All state transitions and audit records are written here.
func (p *Pool) executeJob(ctx context.Context, task *jobTask, logger *slog.Logger) {
	job := task.job
	logger = logger.With("job_id", job.ID, "job_name", job.Name, "attempt", job.Attempt)

	// Transition: scheduled → running (CAS in PostgreSQL)
	if err := p.store.MarkJobRunning(ctx, job.ID, p.cfg.WorkerID); err != nil {
		logger.Error("failed to mark job running", "err", err)
		_ = task.ackFn(err)
		return
	}

	// Find the right executor for this job type
	executor := p.resolveExecutor(job.Type)
	if executor == nil {
		msg := fmt.Sprintf("no executor for job type %s", job.Type)
		logger.Error(msg)
		_ = p.store.MarkJobFailed(ctx, job.ID, msg, p.nextRetryTime(job))
		_ = task.ackFn(fmt.Errorf("%s", msg))
		return
	}

	logger.Info("executing job")

	// Apply per-job deadline if configured
	execCtx := ctx
	if job.Deadline != nil {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithDeadline(ctx, *job.Deadline)
		defer cancel()
	}

	err := executor.Execute(execCtx, job)

	if err != nil {
		logger.Warn("job execution failed", "err", err)
		retryAt := p.nextRetryTime(job)
		_ = p.store.MarkJobFailed(ctx, job.ID, err.Error(), retryAt)
		_ = task.ackFn(err) // NACK: do not remove from Redis PEL
		return
	}

	logger.Info("job completed successfully")
	_ = p.store.MarkJobCompleted(ctx, job.ID)
	_ = task.ackFn(nil) // ACK: remove from Redis PEL permanently
}

// resolveExecutor finds the first executor that can handle the given job type.
func (p *Pool) resolveExecutor(jobType domain.JobType) Executor {
	for _, e := range p.executors {
		if e.CanExecute(jobType) {
			return e
		}
	}
	return nil
}

// nextRetryTime computes the next retry timestamp using full-jitter backoff.
// Returns nil if the job has exhausted its max retries.
func (p *Pool) nextRetryTime(job *domain.Job) *time.Time {
	if !job.IsRetryable() {
		return nil
	}
	delay := retry.FullJitterBackoff(job.Attempt, 5*time.Second, 30*time.Minute)
	t := time.Now().Add(delay)
	return &t
}

// drain closes the job channel and waits for all in-flight jobs to complete,
// up to ShutdownTimeout. Returns error if timeout exceeded.
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
		return fmt.Errorf("shutdown timeout exceeded")
	}
}

// heartbeatLoop periodically calls Heartbeat so the scheduler knows this worker is alive.
// On context cancellation (shutdown), deregisters the worker from PostgreSQL.
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

// ActiveCount returns the number of currently executing jobs.
func (p *Pool) ActiveCount() int {
	return int(p.activeCount.Load())
}
