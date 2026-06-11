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
	"github.com/shreeharshshinde/orion/internal/worker/cancel"
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
	wg          sync.WaitGroup // tracks worker goroutines
	dequeueWg   sync.WaitGroup // tracks dequeue goroutines; drained before jobCh is closed

	// cancel registry: maps job ID → cancel func for in-flight executions.
	cancelMu       sync.Mutex
	cancelRegistry map[uuid.UUID]context.CancelFunc
	cancelSignaler cancel.Signaler // may be nil (no cross-process cancel)
}

type jobTask struct {
	job   *domain.Job
	ackFn queue.AckFunc
}

// NewPool creates a Pool. Call Start() to begin processing jobs.
// m may be nil in tests — all metric calls are nil-guarded.
// sig may be nil — cancel signals will only work within the same process.
func NewPool(cfg WorkerConfig, q queue.Queue, s store.Store, executors []Executor, m *observability.Metrics, logger *slog.Logger, sig ...cancel.Signaler) *Pool {
	p := &Pool{
		cfg:            cfg,
		queue:          q,
		store:          s,
		executors:      executors,
		metrics:        m,
		logger:         logger,
		jobCh:          make(chan *jobTask, cfg.Concurrency),
		cancelRegistry: make(map[uuid.UUID]context.CancelFunc),
	}
	if len(sig) > 0 {
		p.cancelSignaler = sig[0]
	}
	return p
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

	// Listen for cross-process cancel signals if a signaler is configured.
	if p.cancelSignaler != nil {
		go p.startCancelListener(ctx)
	}

	// Run the dequeue loop in the calling goroutine (blocks until ctx cancelled).
	p.dequeueLoop(ctx)

	// ctx cancelled: drain remaining in-flight jobs.
	return p.drain()
}

// dequeueLoop pulls jobs from every configured queue and dispatches them to jobCh.
// Sending to jobCh blocks when all worker slots are occupied — natural backpressure.
func (p *Pool) dequeueLoop(ctx context.Context) {
	for _, queueName := range p.cfg.QueueNames {
		p.dequeueWg.Add(1)
		go func(qName string) {
			defer p.dequeueWg.Done()
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
	<-ctx.Done()
}

// registerCancel stores a cancel func for a running job.
func (p *Pool) registerCancel(id uuid.UUID, cancel context.CancelFunc) {
	p.cancelMu.Lock()
	p.cancelRegistry[id] = cancel
	p.cancelMu.Unlock()
}

// deregisterCancel removes the cancel func when a job finishes.
func (p *Pool) deregisterCancel(id uuid.UUID) {
	p.cancelMu.Lock()
	delete(p.cancelRegistry, id)
	p.cancelMu.Unlock()
}

// CancelJob cancels an in-flight job by ID. Returns false if the job is not
// currently running on this worker.
func (p *Pool) CancelJob(id uuid.UUID) bool {
	p.cancelMu.Lock()
	fn, ok := p.cancelRegistry[id]
	p.cancelMu.Unlock()
	if ok {
		fn()
	}
	return ok
}

// startCancelListener subscribes to Redis cancel signals and cancels matching
// in-flight jobs on this worker.
func (p *Pool) startCancelListener(ctx context.Context) {
	ch := p.cancelSignaler.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-ch:
			if !ok {
				return
			}
			p.CancelJob(id)
		}
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

	logger = observability.WithTrace(ctx, logger)

	// ── Transition: scheduled → running ──────────────────────────────────────
	if err := p.store.MarkJobRunning(ctx, job.ID, p.cfg.WorkerID); err != nil {
		if !errors.Is(err, store.ErrStateConflict) {
			logger.Error("failed to mark job running", "err", err)
			span.SetStatus(codes.Error, "MarkJobRunning failed: "+err.Error())
			_ = task.ackFn(err)
			return
		}

		// ErrStateConflict: the job is no longer in 'scheduled' state.
		// This happens on worker restart: the PEL redelivers a message for a job
		// that this worker already transitioned to 'running' before crashing.
		// Fetch the current state to decide how to proceed.
		current, fetchErr := p.store.GetJob(ctx, job.ID)
		if fetchErr != nil {
			logger.Error("state conflict on MarkJobRunning, could not fetch job", "err", fetchErr)
			_ = task.ackFn(fetchErr)
			return
		}

		switch {
		case current.Status == domain.JobStatusRunning && current.WorkerID == p.cfg.WorkerID:
			// Restart scenario: this worker owns the job. Proceed with execution
			// as if MarkJobRunning had succeeded — the DB state is already correct.
			logger.Info("resuming job owned by this worker after restart", "job_id", job.ID)

		case current.Status == domain.JobStatusRunning:
			// A different worker is executing this job. ACK to remove the stale
			// PEL message — the other worker will complete or fail it normally.
			logger.Warn("job already running on another worker, skipping",
				"job_id", job.ID, "owner_worker_id", current.WorkerID)
			_ = task.ackFn(nil)
			return

		default:
			// Job reached a terminal or unexpected state (completed, failed, cancelled).
			// ACK the stale PEL message and move on.
			logger.Info("job in unexpected state after MarkJobRunning conflict, skipping",
				"job_id", job.ID, "status", current.Status)
			_ = task.ackFn(nil)
			return
		}
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

	// ── Register cancel func so the job can be cancelled mid-execution ────────
	execCtx, jobCancel := context.WithCancel(execCtx)
	p.registerCancel(job.ID, jobCancel)
	defer func() {
		jobCancel()
		p.deregisterCancel(job.ID)
	}()

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

		// If the job was cancelled (context cancelled AND DB status is now cancelled),
		// transition to cancelled rather than failed.
		if errors.Is(err, context.Canceled) {
			current, dbErr := p.store.GetJob(ctx, job.ID)
			if dbErr == nil && current.Status == domain.JobStatusCancelled {
				_ = p.store.RecordExecution(ctx, &domain.JobExecution{
					ID:         uuid.New(),
					JobID:      job.ID,
					Attempt:    job.Attempt + 1,
					WorkerID:   p.cfg.WorkerID,
					Status:     domain.JobStatusCancelled,
					StartedAt:  &startedAt,
					FinishedAt: &finishedAt,
					Error:      "cancelled",
				})
				_ = task.ackFn(nil) // ACK: job is done, no redelivery needed
				return
			}
		}

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

// drain waits for all dequeue goroutines to exit, then closes jobCh and waits
// for all worker goroutines to finish. Closing jobCh only after dequeue goroutines
// have exited guarantees no goroutine can send on a closed channel.
func (p *Pool) drain() error {
	p.logger.Info("draining worker pool")

	// Wait for every dequeue goroutine to exit before closing jobCh.
	// Each goroutine exits its loop when ctx is cancelled (already done by the
	// time drain() is called from Start). The worst-case wait is one XREADGROUP
	// block timeout (~5s), after which the goroutine checks ctx.Done() and returns.
	p.dequeueWg.Wait()

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
