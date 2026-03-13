package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
)

// JobStore defines all persistence operations for jobs.
// The PostgreSQL implementation lives in store/postgres.
// This interface is what the scheduler, API, and worker depend on.
type JobStore interface {
	// CreateJob inserts a new job. Returns existing job if idempotency_key matches.
	CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error)

	// GetJob retrieves a single job by ID.
	GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error)

	// GetJobByIdempotencyKey looks up a job by client-provided idempotency key.
	GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error)

	// TransitionJobState performs a conditional UPDATE using CAS semantics.
	// Returns ErrStateConflict if the job is not in expectedStatus.
	// This is the ONLY way job status should ever be mutated.
	TransitionJobState(ctx context.Context, id uuid.UUID, expectedStatus, newStatus domain.JobStatus, opts ...TransitionOption) error

	// ListJobs returns jobs matching the filter, ordered by priority DESC, created_at ASC.
	ListJobs(ctx context.Context, filter JobFilter) ([]*domain.Job, error)

	// ClaimPendingJobs atomically claims up to limit jobs from the specified queue.
	// Sets status=scheduled and worker_id=workerID. Returns the claimed jobs.
	ClaimPendingJobs(ctx context.Context, queueName, workerID string, limit int) ([]*domain.Job, error)

	// MarkJobRunning transitions a job to running and records start time.
	MarkJobRunning(ctx context.Context, id uuid.UUID, workerID string) error

	// MarkJobCompleted transitions a job to completed and records end time.
	MarkJobCompleted(ctx context.Context, id uuid.UUID) error

	// MarkJobFailed transitions a job to failed, records error, increments attempt.
	MarkJobFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt *time.Time) error

	// ReclaimOrphanedJobs finds running jobs whose workers have gone silent
	// beyond the given staleness threshold and requeues them.
	ReclaimOrphanedJobs(ctx context.Context, staleThreshold time.Duration) (int, error)

	// DeleteJob hard-deletes a job (admin use only; prefer cancellation).
	DeleteJob(ctx context.Context, id uuid.UUID) error
}

// ExecutionStore manages the append-only job execution audit log.
type ExecutionStore interface {
	// RecordExecution inserts a new execution attempt record. Never updates.
	RecordExecution(ctx context.Context, exec *domain.JobExecution) error

	// GetExecutions returns all execution attempts for a given job, ordered by attempt ASC.
	GetExecutions(ctx context.Context, jobID uuid.UUID) ([]*domain.JobExecution, error)
}

// WorkerStore manages worker registration and heartbeats.
type WorkerStore interface {
	// RegisterWorker upserts a worker record.
	RegisterWorker(ctx context.Context, worker *domain.Worker) error

	// Heartbeat updates the last_heartbeat timestamp for a worker.
	Heartbeat(ctx context.Context, workerID string) error

	// ListActiveWorkers returns workers that have heartbeated within the given TTL.
	ListActiveWorkers(ctx context.Context, ttl time.Duration) ([]*domain.Worker, error)

	// DeregisterWorker marks a worker as offline.
	DeregisterWorker(ctx context.Context, workerID string) error
}

// Store composes all store interfaces. The postgres.DB struct implements this.
type Store interface {
	JobStore
	ExecutionStore
	WorkerStore
}

// JobFilter controls which jobs are returned by ListJobs.
type JobFilter struct {
	Status    *domain.JobStatus
	QueueName *string
	WorkerID  *string
	Limit     int
	Offset    int
}

// TransitionOption allows callers to pass additional update fields
// alongside a state transition (e.g., setting WorkerID, ErrorMessage).
type TransitionOption func(*transitionUpdate)

type transitionUpdate struct {
	workerID     *string
	errorMessage *string
	nextRetryAt  *time.Time
	completedAt  *time.Time
	startedAt    *time.Time
}

func WithWorkerID(id string) TransitionOption {
	return func(u *transitionUpdate) { u.workerID = &id }
}

func WithError(msg string) TransitionOption {
	return func(u *transitionUpdate) { u.errorMessage = &msg }
}

func WithNextRetryAt(t time.Time) TransitionOption {
	return func(u *transitionUpdate) { u.nextRetryAt = &t }
}

// Sentinel errors for store operations.
var (
	ErrNotFound     = &StoreError{Code: "NOT_FOUND"}
	ErrStateConflict = &StoreError{Code: "STATE_CONFLICT"} // CAS failed
	ErrDuplicate    = &StoreError{Code: "DUPLICATE"}       // idempotency key collision
)

type StoreError struct {
	Code    string
	Message string
}

func (e *StoreError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

func (e *StoreError) Is(target error) bool {
	t, ok := target.(*StoreError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}