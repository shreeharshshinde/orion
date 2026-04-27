package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/observability"
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
	metrics   *observability.Metrics // Phase 6: Prometheus metrics
	logger    *slog.Logger

	jobCh       chan *jobTask
	activeCount atomic.Int32
	wg          sync.WaitGroup
}

type jobTask struct {
	job   *domain.Job
	ackFn queue.AckFunc
}

// NewPool creates a Pool. Call Start() to begin processing jobs.
// m may be nil in tests — all metric calls are nil-guarded.
func NewPool(cfg WorkerConfig, q queue.Queue, s store.Store, executors []Executor, m *observability.Metrics, logger *slog.Logger) *Pool {
	return &Pool{
		cfg:       cfg,
		queue:     q,
		store:     s,
		executors: executors,
		metrics:   m,
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
			p.setActiveJobsGauge()
			p.executeJob(ctx, task, logger)
			p.activeCount.Add(-1)
			p.setActiveJobsGauge()

		case <-ctx.Done():
			// Drain any tasks already in the channel before exiting.
			for {
				select {
				case task, ok := <-p.jobCh:
					if !ok {
						return
					}
					p.activeCount.Add(1)
					p.setActiveJobsGauge()
					p.executeJob(context.Background(), task, logger)
					p.activeCount.Add(-1)
					p.setActiveJobsGauge()
				default:
					return
				}
			}
		}
	}
}

// executeJob runs a single job through the appropriate executor.
// Phase 6: adds OpenTelemetry span + Prometheus metrics to every execution path.
func (p *Pool) executeJob(ctx context.Context, task *jobTask, logger *slog.Logger) {
	job := task.job
	logger = logger.With("job_id", job.ID, "job_name", job.Name, "attempt", job.Attempt)
	startedAt := time.Now()

	// ── Span: wrap the full job execution ────────────────────────────────────
	// This span links to the scheduler's dispatch_job span via the trace context
	// propagated through the job payload (set by the scheduler when enqueueing).
	ctx, span := observability.Tracer("orion.worker").Start(ctx, "worker.execute_job",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("job.id", job.ID.String()),
			attribute.String("job.name", job.Name),
			attribute.String("job.type", string(job.Type)),
			attribute.String("job.queue", job.QueueName),
			attribute.Int("job.attempt", job.Attempt),
			semconv.MessagingSystemKey.String("redis"),
		),
	)
	defer span.End()

	// ── Transition: scheduled → running ──────────────────────────────────────
	if err := p.store.MarkJobRunning(ctx, job.ID, p.cfg.WorkerID); err != nil {
		logger.Error("failed to mark job running", "err", err)
		span.SetStatus(codes.Error, "MarkJobRunning failed: "+err.Error())
		_ = task.ackFn(err)
		return
	}

	// ── Audit: record execution start ─────────────────────────────────────────
	_ = p.store.RecordExecution(ctx, &domain.JobExecution{
		ID:        uuid.New(),
		JobID:     job.ID,
		Attempt:   job.Attempt + 1,
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
		span.SetStatus(codes.Error, msg)
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
		// Count as config_error failure
		if p.metrics != nil {
			p.metrics.JobsFailed.WithLabelValues(job.QueueName, string(job.Type), "config_error").Inc()
		}
		_ = task.ackFn(fmt.Errorf("%s", msg))
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

	// ── Execute ───────────────────────────────────────────────────────────────
	err := executor.Execute(execCtx, job)
	finishedAt := time.Now()
	elapsed := finishedAt.Sub(startedAt).Seconds()

	// ── Handle result ─────────────────────────────────────────────────────────
	if err != nil {
		logger.Warn("job execution failed",
			"err", err,
			"elapsed_ms", finishedAt.Sub(startedAt).Milliseconds(),
		)

		// Classify failure reason for the metric label.
		// reason is low-cardinality: 3 possible values.
		reason := "handler_error"
		if errors.Is(err, context.Canceled) {
			reason = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			reason = "deadline_exceeded"
		}

		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(
			attribute.String("error.reason", reason),
			attribute.Float64("job.elapsed_seconds", elapsed),
		)

		// Metrics: failure counters + duration histogram
		if p.metrics != nil {
			p.metrics.JobsFailed.WithLabelValues(job.QueueName, string(job.Type), reason).Inc()
			p.metrics.JobDuration.WithLabelValues(job.QueueName, string(job.Type), "failed").Observe(elapsed)

			// Is this the terminal failure? If so, count as dead.
			if !job.IsRetryable() {
				p.metrics.JobsDead.WithLabelValues(job.QueueName).Inc()
			} else {
				p.metrics.JobsRetried.WithLabelValues(job.QueueName).Inc()
			}
		}

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

	// ── Success ───────────────────────────────────────────────────────────────
	logger.Info("job completed successfully",
		"elapsed_ms", finishedAt.Sub(startedAt).Milliseconds(),
	)

	span.SetAttributes(attribute.Float64("job.elapsed_seconds", elapsed))

	if p.metrics != nil {
		p.metrics.JobsCompleted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
		p.metrics.JobDuration.WithLabelValues(job.QueueName, string(job.Type), "completed").Observe(elapsed)
	}

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

// setActiveJobsGauge syncs the atomic counter to the Prometheus gauge.
func (p *Pool) setActiveJobsGauge() {
	if p.metrics != nil {
		p.metrics.WorkerActiveJobs.WithLabelValues(p.cfg.WorkerID).Set(
			float64(p.activeCount.Load()),
		)
	}
}

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
