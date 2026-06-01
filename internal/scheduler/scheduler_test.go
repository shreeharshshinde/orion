package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// fakeStore — implements store.Store with controllable function fields
// ─────────────────────────────────────────────────────────────────────────────

type fakeStore struct {
	mu                  sync.Mutex
	listJobsFn          func(ctx context.Context, f store.JobFilter) ([]*domain.Job, error)
	listRetryableFn     func(ctx context.Context, limit int) ([]*domain.Job, error)
	transitionFn        func(ctx context.Context, id uuid.UUID, exp, next domain.JobStatus, opts ...store.TransitionOption) error
	reclaimOrphanedFn   func(ctx context.Context, d time.Duration) (int, error)
	transitions         []transitionCall
}

type transitionCall struct {
	ID   uuid.UUID
	From domain.JobStatus
	To   domain.JobStatus
}

func (f *fakeStore) recordTransition(id uuid.UUID, from, to domain.JobStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transitionCall{id, from, to})
}

func (f *fakeStore) getTransitions() []transitionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]transitionCall, len(f.transitions))
	copy(out, f.transitions)
	return out
}

// JobStore
func (f *fakeStore) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
	return job, nil
}
func (f *fakeStore) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) TransitionJobState(ctx context.Context, id uuid.UUID, exp, next domain.JobStatus, opts ...store.TransitionOption) error {
	f.recordTransition(id, exp, next)
	if f.transitionFn != nil {
		return f.transitionFn(ctx, id, exp, next, opts...)
	}
	return nil
}
func (f *fakeStore) ListJobs(ctx context.Context, filter store.JobFilter) ([]*domain.Job, error) {
	if f.listJobsFn != nil {
		return f.listJobsFn(ctx, filter)
	}
	return nil, nil
}
func (f *fakeStore) ClaimPendingJobs(ctx context.Context, q, w string, limit int) ([]*domain.Job, error) {
	return nil, nil
}
func (f *fakeStore) MarkJobRunning(ctx context.Context, id uuid.UUID, w string) error { return nil }
func (f *fakeStore) MarkJobCompleted(ctx context.Context, id uuid.UUID) error         { return nil }
func (f *fakeStore) MarkJobFailed(ctx context.Context, id uuid.UUID, msg string, t *time.Time) error {
	return nil
}
func (f *fakeStore) ReclaimOrphanedJobs(ctx context.Context, d time.Duration) (int, error) {
	if f.reclaimOrphanedFn != nil {
		return f.reclaimOrphanedFn(ctx, d)
	}
	return 0, nil
}
func (f *fakeStore) DeleteJob(ctx context.Context, id uuid.UUID) error { return nil }
func (f *fakeStore) ListRetryableJobs(ctx context.Context, limit int) ([]*domain.Job, error) {
	if f.listRetryableFn != nil {
		return f.listRetryableFn(ctx, limit)
	}
	return nil, nil
}

// ExecutionStore
func (f *fakeStore) RecordExecution(ctx context.Context, exec *domain.JobExecution) error {
	return nil
}
func (f *fakeStore) GetExecutions(ctx context.Context, jobID uuid.UUID) ([]*domain.JobExecution, error) {
	return nil, nil
}

// WorkerStore
func (f *fakeStore) RegisterWorker(ctx context.Context, w *domain.Worker) error { return nil }
func (f *fakeStore) Heartbeat(ctx context.Context, id string) error             { return nil }
func (f *fakeStore) ListActiveWorkers(ctx context.Context, ttl time.Duration) ([]*domain.Worker, error) {
	return nil, nil
}
func (f *fakeStore) DeregisterWorker(ctx context.Context, id string) error { return nil }

// PipelineStore
func (f *fakeStore) CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) ListPipelines(ctx context.Context, filter store.PipelineFilter) ([]*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error {
	return nil
}
func (f *fakeStore) AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error {
	return nil
}
func (f *fakeStore) GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error) {
	return nil, nil
}

// QueueConfigStore
func (f *fakeStore) ListQueueConfigs(_ context.Context) ([]*store.QueueConfig, error) {
	return nil, nil
}
func (f *fakeStore) GetQueueConfig(_ context.Context, _ string) (*store.QueueConfig, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) UpsertQueueConfig(_ context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) {
	return cfg, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// fakeQueue — implements queue.Queue, records Enqueue calls
// ─────────────────────────────────────────────────────────────────────────────

type fakeQueue struct {
	mu       sync.Mutex
	enqueued []*domain.Job
	enqueueFn func(ctx context.Context, job *domain.Job) error
}

func (q *fakeQueue) Enqueue(ctx context.Context, job *domain.Job) error {
	if q.enqueueFn != nil {
		return q.enqueueFn(ctx, job)
	}
	q.mu.Lock()
	q.enqueued = append(q.enqueued, job)
	q.mu.Unlock()
	return nil
}
func (q *fakeQueue) getEnqueued() []*domain.Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*domain.Job, len(q.enqueued))
	copy(out, q.enqueued)
	return out
}
func (q *fakeQueue) Dequeue(ctx context.Context, queueName string, vis time.Duration) (*domain.Job, queue.AckFunc, error) {
	return nil, nil, nil
}
func (q *fakeQueue) Len(ctx context.Context, queueName string) (int64, error)        { return 0, nil }
func (q *fakeQueue) Dead(ctx context.Context, job *domain.Job, reason string) error  { return nil }
func (q *fakeQueue) Flush(ctx context.Context, queueName string) error               { return nil }
func (q *fakeQueue) Close() error                                                     { return nil }
func (q *fakeQueue) StartScheduledSweeper(ctx context.Context)                       {}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newScheduler(s store.Store, q queue.Queue) *Scheduler {
	return New(
		Config{BatchSize: 10, ScheduleInterval: time.Hour, OrphanInterval: time.Hour},
		nil, // db pool — not needed for unit tests (no advisory lock calls)
		s, q,
		nil, nil, nil, nil,
		testLogger(),
	)
}

func queuedJob() *domain.Job {
	return &domain.Job{
		ID:        uuid.New(),
		Status:    domain.JobStatusQueued,
		QueueName: queue.QueueDefault,
		Type:      domain.JobTypeInline,
		Priority:  domain.PriorityNormal,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// scheduleQueuedJobs
// ─────────────────────────────────────────────────────────────────────────────

// TestScheduleQueuedJobs_DispatchesQueuedJobs verifies that queued jobs are
// transitioned to scheduled and enqueued into the queue broker.
func TestScheduleQueuedJobs_DispatchesQueuedJobs(t *testing.T) {
	job1, job2 := queuedJob(), queuedJob()
	fs := &fakeStore{
		listJobsFn: func(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
			return []*domain.Job{job1, job2}, nil
		},
	}
	fq := &fakeQueue{}
	s := newScheduler(fs, fq)

	if err := s.scheduleQueuedJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := len(fq.getEnqueued()); got != 2 {
		t.Errorf("enqueued %d jobs, want 2", got)
	}
	transitions := fs.getTransitions()
	if len(transitions) != 2 {
		t.Fatalf("got %d transitions, want 2", len(transitions))
	}
	for _, tr := range transitions {
		if tr.From != domain.JobStatusQueued || tr.To != domain.JobStatusScheduled {
			t.Errorf("unexpected transition %s→%s", tr.From, tr.To)
		}
	}
}

// TestScheduleQueuedJobs_StateConflictSkipsJob verifies that a CAS conflict on
// one job does not prevent the remaining jobs from being dispatched.
func TestScheduleQueuedJobs_StateConflictSkipsJob(t *testing.T) {
	conflictID := uuid.New()
	conflictJob := &domain.Job{ID: conflictID, Status: domain.JobStatusQueued, QueueName: queue.QueueDefault}
	goodJob := queuedJob()

	fs := &fakeStore{
		listJobsFn: func(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
			return []*domain.Job{conflictJob, goodJob}, nil
		},
		transitionFn: func(_ context.Context, id uuid.UUID, _, _ domain.JobStatus, _ ...store.TransitionOption) error {
			if id == conflictID {
				return store.ErrStateConflict
			}
			return nil
		},
	}
	fq := &fakeQueue{}
	s := newScheduler(fs, fq)

	if err := s.scheduleQueuedJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	enqueued := fq.getEnqueued()
	if len(enqueued) != 1 || enqueued[0].ID != goodJob.ID {
		t.Errorf("expected only goodJob enqueued, got %v", enqueued)
	}
}

// TestScheduleQueuedJobs_EnqueueFailureRollsBack verifies that when Enqueue
// fails, the job is rolled back from scheduled → queued.
func TestScheduleQueuedJobs_EnqueueFailureRollsBack(t *testing.T) {
	job := queuedJob()
	fs := &fakeStore{
		listJobsFn: func(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
			return []*domain.Job{job}, nil
		},
	}
	fq := &fakeQueue{
		enqueueFn: func(_ context.Context, _ *domain.Job) error {
			return errors.New("redis down")
		},
	}
	s := newScheduler(fs, fq)

	if err := s.scheduleQueuedJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	transitions := fs.getTransitions()
	// Expect: queued→scheduled (attempt), then scheduled→queued (rollback)
	if len(transitions) != 2 {
		t.Fatalf("got %d transitions, want 2", len(transitions))
	}
	if transitions[0].From != domain.JobStatusQueued || transitions[0].To != domain.JobStatusScheduled {
		t.Errorf("first transition should be queued→scheduled, got %s→%s", transitions[0].From, transitions[0].To)
	}
	if transitions[1].From != domain.JobStatusScheduled || transitions[1].To != domain.JobStatusQueued {
		t.Errorf("second transition should be scheduled→queued (rollback), got %s→%s", transitions[1].From, transitions[1].To)
	}
}

// TestScheduleQueuedJobs_EmptyQueue verifies no error and no enqueues when
// there are no queued jobs.
func TestScheduleQueuedJobs_EmptyQueue(t *testing.T) {
	fs := &fakeStore{}
	fq := &fakeQueue{}
	s := newScheduler(fs, fq)

	if err := s.scheduleQueuedJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(fq.getEnqueued()); got != 0 {
		t.Errorf("expected 0 enqueued, got %d", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// promoteRetryableJobs
// ─────────────────────────────────────────────────────────────────────────────

// TestPromoteRetryableJobs_PromotesDueJobs verifies that failed jobs whose
// next_retry_at has passed are transitioned failed→retrying→queued.
func TestPromoteRetryableJobs_PromotesDueJobs(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	job := &domain.Job{
		ID:          uuid.New(),
		Status:      domain.JobStatusFailed,
		Attempt:     1,
		MaxRetries:  3,
		NextRetryAt: &past,
	}
	fs := &fakeStore{
		listRetryableFn: func(_ context.Context, _ int) ([]*domain.Job, error) {
			return []*domain.Job{job}, nil
		},
	}
	s := newScheduler(fs, &fakeQueue{})

	if err := s.promoteRetryableJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	transitions := fs.getTransitions()
	if len(transitions) != 2 {
		t.Fatalf("got %d transitions, want 2", len(transitions))
	}
	if transitions[0].From != domain.JobStatusFailed || transitions[0].To != domain.JobStatusRetrying {
		t.Errorf("first transition should be failed→retrying, got %s→%s", transitions[0].From, transitions[0].To)
	}
	if transitions[1].From != domain.JobStatusRetrying || transitions[1].To != domain.JobStatusQueued {
		t.Errorf("second transition should be retrying→queued, got %s→%s", transitions[1].From, transitions[1].To)
	}
}

// TestPromoteRetryableJobs_NoDueJobs verifies no transitions when the store
// returns no retryable jobs (all still in backoff window).
func TestPromoteRetryableJobs_NoDueJobs(t *testing.T) {
	fs := &fakeStore{
		listRetryableFn: func(_ context.Context, _ int) ([]*domain.Job, error) {
			return nil, nil
		},
	}
	s := newScheduler(fs, &fakeQueue{})

	if err := s.promoteRetryableJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(fs.getTransitions()); got != 0 {
		t.Errorf("expected 0 transitions, got %d", got)
	}
}

// TestPromoteRetryableJobs_StateConflictContinues verifies that a CAS conflict
// on one job does not stop promotion of subsequent jobs.
func TestPromoteRetryableJobs_StateConflictContinues(t *testing.T) {
	conflictID := uuid.New()
	past := time.Now().Add(-time.Minute)
	conflictJob := &domain.Job{ID: conflictID, Status: domain.JobStatusFailed, NextRetryAt: &past}
	goodJob := &domain.Job{ID: uuid.New(), Status: domain.JobStatusFailed, NextRetryAt: &past}

	fs := &fakeStore{
		listRetryableFn: func(_ context.Context, _ int) ([]*domain.Job, error) {
			return []*domain.Job{conflictJob, goodJob}, nil
		},
		transitionFn: func(_ context.Context, id uuid.UUID, _, _ domain.JobStatus, _ ...store.TransitionOption) error {
			if id == conflictID {
				return store.ErrStateConflict
			}
			return nil
		},
	}
	s := newScheduler(fs, &fakeQueue{})

	if err := s.promoteRetryableJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// conflictJob: 1 failed transition (conflict on failed→retrying, stops there)
	// goodJob: 2 successful transitions (failed→retrying, retrying→queued)
	transitions := fs.getTransitions()
	if len(transitions) != 3 {
		t.Errorf("got %d transitions, want 3", len(transitions))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// reclaimOrphanedJobs
// ─────────────────────────────────────────────────────────────────────────────

// TestReclaimOrphanedJobs_CallsStoreWithCorrectThreshold verifies the store is
// called with 2× the worker heartbeat TTL.
func TestReclaimOrphanedJobs_CallsStoreWithCorrectThreshold(t *testing.T) {
	var gotDuration time.Duration
	fs := &fakeStore{
		reclaimOrphanedFn: func(_ context.Context, d time.Duration) (int, error) {
			gotDuration = d
			return 3, nil
		},
	}
	s := newScheduler(fs, &fakeQueue{})

	if err := s.reclaimOrphanedJobs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := workerHeartbeatTTL * 2
	if gotDuration != want {
		t.Errorf("reclaim called with threshold %v, want %v", gotDuration, want)
	}
}

// TestReclaimOrphanedJobs_StoreError propagates the error from the store.
func TestReclaimOrphanedJobs_StoreError(t *testing.T) {
	fs := &fakeStore{
		reclaimOrphanedFn: func(_ context.Context, _ time.Duration) (int, error) {
			return 0, errors.New("db error")
		},
	}
	s := newScheduler(fs, &fakeQueue{})

	if err := s.reclaimOrphanedJobs(context.Background()); err == nil {
		t.Error("expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Leader election contention
// ─────────────────────────────────────────────────────────────────────────────

// TestRun_ContextCancelledBeforeLockAcquired verifies that Run returns
// ctx.Err() immediately when the context is already cancelled, without
// attempting to acquire the advisory lock (which requires a real DB).
func TestRun_ContextCancelledBeforeLockAcquired(t *testing.T) {
	s := newScheduler(&fakeStore{}, &fakeQueue{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	err := s.Run(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestRunAsLeader_ExitsOnContextCancel verifies that runAsLeader returns
// promptly when ctx is cancelled, without requiring a real DB connection.
func TestRunAsLeader_ExitsOnContextCancel(t *testing.T) {
	s := newScheduler(&fakeStore{}, &fakeQueue{})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.runAsLeader(ctx, nil) // nil conn — releaseLeaderLock is a no-op for nil
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runAsLeader did not exit after context cancellation")
	}
}
