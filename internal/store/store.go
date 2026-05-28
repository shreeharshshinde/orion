// Package store defines the persistence interfaces for Orion.
//
// Architecture rule: NO SQL lives here. This package only defines contracts
// (interfaces) and shared types. The actual SQL lives in store/postgres/db.go
// and store/postgres/pipeline.go.
// This separation means you can swap PostgreSQL for any other database without
// touching the scheduler, worker, or API handler — they all use this interface.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
)

// ============================================================
// JobStore
// ============================================================

type JobStore interface {
	// CreateJob inserts a new job. Handles idempotency: if job.IdempotencyKey is
	// set and a matching row already exists, the existing job is returned unchanged.
	// Returns ErrDuplicate on a concurrent-insert race that is resolved by re-fetch.
	CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error)

	// GetJob fetches a single job by its UUID primary key.
	// Returns ErrNotFound if no row exists.
	GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error)

	// GetJobByIdempotencyKey looks up a job by the client-supplied idempotency key.
	// Returns ErrNotFound if the key has never been used.
	GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error)

	// TransitionJobState is the single CAS (Compare-And-Swap) state transition.
	// Updates status from expectedStatus → newStatus only if the current DB status
	// matches expectedStatus. Returns ErrStateConflict if the CAS guard fails
	// (another process already transitioned the job). Optional TransitionOption
	// funcs set additional fields (worker_id, timestamps, error) atomically.
	TransitionJobState(ctx context.Context, id uuid.UUID, expectedStatus, newStatus domain.JobStatus, opts ...TransitionOption) error

	// ListJobs returns jobs matching the filter, ordered by priority DESC, created_at ASC.
	// Used by the scheduler dispatch loop and the GET /jobs API endpoint.
	ListJobs(ctx context.Context, filter JobFilter) ([]*domain.Job, error)

	// ClaimPendingJobs atomically claims up to limit queued jobs for a worker using
	// SELECT FOR UPDATE SKIP LOCKED. Guarantees no two workers receive the same job.
	// Called by the worker pool's dequeue loop.
	ClaimPendingJobs(ctx context.Context, queueName, workerID string, limit int) ([]*domain.Job, error)

	// MarkJobRunning transitions scheduled → running, setting worker_id and started_at.
	// Thin wrapper around TransitionJobState. Returns ErrStateConflict if another
	// worker already claimed the job.
	MarkJobRunning(ctx context.Context, id uuid.UUID, workerID string) error

	// MarkJobCompleted transitions running → completed, setting completed_at.
	// Called by the worker after the executor returns nil.
	MarkJobCompleted(ctx context.Context, id uuid.UUID) error

	// MarkJobFailed transitions running → failed, incrementing attempt and setting
	// error_message and next_retry_at. nextRetryAt may be nil for non-retryable jobs.
	// Called by the worker after the executor returns a non-nil error.
	MarkJobFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt *time.Time) error

	// ReclaimOrphanedJobs resets running jobs whose worker has missed heartbeats
	// (last_heartbeat older than staleThreshold) back to queued. Returns the count
	// of reclaimed jobs. Called by the scheduler's orphan sweeper every 30s.
	ReclaimOrphanedJobs(ctx context.Context, staleThreshold time.Duration) (int, error)

	// DeleteJob hard-deletes a job by ID. Prefer cancelling over deleting to preserve
	// the execution audit trail. Returns ErrNotFound if the job does not exist.
	DeleteJob(ctx context.Context, id uuid.UUID) error

	// ListRetryableJobs returns failed jobs whose next_retry_at <= NOW() and
	// attempt < max_retries, ordered by next_retry_at ASC (earliest-due first).
	// Uses the idx_jobs_retry_eligible partial index — does not scan all failed jobs.
	// Called by the scheduler's retry promoter on every 2s tick.
	ListRetryableJobs(ctx context.Context, limit int) ([]*domain.Job, error)
}

// ============================================================
// ExecutionStore
// ============================================================

type ExecutionStore interface {
	RecordExecution(ctx context.Context, exec *domain.JobExecution) error
	GetExecutions(ctx context.Context, jobID uuid.UUID) ([]*domain.JobExecution, error)
}

// ============================================================
// WorkerStore
// ============================================================

type WorkerStore interface {
	RegisterWorker(ctx context.Context, worker *domain.Worker) error
	Heartbeat(ctx context.Context, workerID string) error
	ListActiveWorkers(ctx context.Context, ttl time.Duration) ([]*domain.Worker, error)
	DeregisterWorker(ctx context.Context, workerID string) error
}

// ============================================================
// PipelineStore — Phase 5
// ============================================================

// PipelineStore manages the lifecycle of pipelines and their node-to-job mappings.
//
// Design note: pipeline SQL lives in store/postgres/pipeline.go, separate from
// the job SQL in store/postgres/db.go. Both are in the same `postgres` package
// and implement the Store interface together. The split keeps each file focused.
type PipelineStore interface {
	// CreatePipeline inserts a new pipeline in pending status.
	// The DAGSpec is serialized as JSONB — no migration needed when spec shape changes.
	CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error)

	// GetPipeline retrieves a pipeline by ID. Returns ErrNotFound if absent.
	GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error)

	// ListPipelines returns pipelines matching the filter, ordered by created_at DESC.
	ListPipelines(ctx context.Context, filter PipelineFilter) ([]*domain.Pipeline, error)

	// ListPipelinesByStatus returns pipelines in a specific status, ordered by
	// created_at ASC (oldest first — FIFO fairness across concurrent pipelines).
	// Called by the scheduler on every tick to find pipelines needing advancement.
	ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error)

	// UpdatePipelineStatus transitions a pipeline to a new status.
	// Automatically sets completed_at for terminal states (completed, failed, cancelled).
	// Returns ErrNotFound if the pipeline does not exist.
	UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error

	// AddPipelineJob links a created job to a pipeline node.
	// ON CONFLICT DO NOTHING makes this idempotent — safe to call twice
	// if the scheduler crashes between CreateJob and AddPipelineJob.
	AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error

	// GetPipelineJobs returns all node-to-job mappings for a pipeline, joined
	// with the current job status. One query instead of N+1 — the advancement
	// algorithm reads this once per tick per pipeline.
	GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*PipelineJobStatus, error)
}

// PipelineFilter controls which pipelines are returned by ListPipelines.
// Nil pointer fields are ignored (not included in the WHERE clause).
type PipelineFilter struct {
	Status *domain.PipelineStatus
	Limit  int
	Offset int
}

// PipelineJobStatus is the result of joining pipeline_jobs with jobs.
// It carries everything the advancement algorithm needs in a single query.
//
// The advancement algorithm reads this struct to:
//   - Build the completed set (JobStatus == "completed" → node is done)
//   - Detect failures (JobStatus == "dead" → cascade cancel downstream)
//   - Know which nodes already have jobs (alreadyCreated guard)
type PipelineJobStatus struct {
	NodeID    string           `json:"node_id"`    // logical name within the DAG
	JobID     uuid.UUID        `json:"job_id"`     // the actual job created for this node
	JobName   string           `json:"job_name"`   // human-readable, e.g. "my-pipeline/preprocess"
	JobStatus domain.JobStatus `json:"job_status"` // current status from the jobs table
}

// ============================================================
// QueueConfigStore — Phase 8
// ============================================================

// QueueConfig is the runtime configuration for one queue.
// Stored in the queue_config PostgreSQL table; reloaded by the scheduler
// on each tick and exposed via the /queues REST API.
type QueueConfig struct {
	QueueName     string    `json:"queue_name"`
	MaxConcurrent int       `json:"max_concurrent"`
	Weight        float64   `json:"weight"`
	RatePerSec    float64   `json:"rate_per_sec"`
	Burst         int       `json:"burst"`
	Enabled       bool      `json:"enabled"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// QueueConfigStore manages per-queue rate limiting and scheduling configuration.
type QueueConfigStore interface {
	// ListQueueConfigs returns all queue configurations (enabled and disabled).
	ListQueueConfigs(ctx context.Context) ([]*QueueConfig, error)

	// GetQueueConfig returns the configuration for a single queue.
	// Returns ErrNotFound if the queue_name does not exist.
	GetQueueConfig(ctx context.Context, queueName string) (*QueueConfig, error)

	// UpsertQueueConfig creates or updates a queue configuration row.
	// Sets updated_at = NOW() automatically.
	UpsertQueueConfig(ctx context.Context, cfg *QueueConfig) (*QueueConfig, error)
}

// ============================================================
// Store — composite interface
// ============================================================

// Store composes all sub-interfaces.
// postgres.DB implements this. In tests, pass a fake/mock implementation.
type Store interface {
	JobStore
	ExecutionStore
	WorkerStore
	PipelineStore      // added Phase 5
	QueueConfigStore   // added Phase 8
}

// ============================================================
// JobFilter
// ============================================================

// JobFilter controls which jobs are returned by ListJobs.
// Nil pointer fields are ignored (not included in the WHERE clause).
type JobFilter struct {
	Status    *domain.JobStatus
	QueueName *string
	WorkerID  *string
	Limit     int
	Offset    int
}

// ============================================================
// TransitionOption — functional options for TransitionJobState
// ============================================================

// TransitionOption is a functional option that sets additional fields
// alongside a job state transition.
type TransitionOption func(*TransitionUpdateExported)

// TransitionUpdateExported holds optional fields that can be set
// atomically with a status transition. Exported so postgres package
// can read field values when building the dynamic SET clause.
type TransitionUpdateExported struct {
	WorkerID     *string
	ErrorMessage *string
	NextRetryAt  *time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

func WithWorkerID(id string) TransitionOption {
	return func(u *TransitionUpdateExported) { u.WorkerID = &id }
}

func WithError(msg string) TransitionOption {
	return func(u *TransitionUpdateExported) { u.ErrorMessage = &msg }
}

func WithNextRetryAt(t time.Time) TransitionOption {
	return func(u *TransitionUpdateExported) { u.NextRetryAt = &t }
}

func WithStartedAt(t time.Time) TransitionOption {
	return func(u *TransitionUpdateExported) { u.StartedAt = &t }
}

func WithCompletedAt(t time.Time) TransitionOption {
	return func(u *TransitionUpdateExported) { u.CompletedAt = &t }
}

// ============================================================
// Sentinel errors
// ============================================================

var (
	ErrNotFound      = &StoreError{Code: "NOT_FOUND"}
	ErrStateConflict = &StoreError{Code: "STATE_CONFLICT"}
	ErrDuplicate     = &StoreError{Code: "DUPLICATE"}
)

// StoreError is the typed error returned by all store operations.
// Use errors.Is(err, store.ErrNotFound) — never compare by string.
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

// Is enables errors.Is() to match any StoreError with the same Code,
// regardless of the Message field. This allows:
//
//	errors.Is(err, store.ErrNotFound) // true for any NOT_FOUND error
func (e *StoreError) Is(target error) bool {
	t, ok := target.(*StoreError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// ============================================================
// Convenience error check helpers
// ============================================================

// IsNotFound returns true if err is a NOT_FOUND StoreError.
// Use instead of errors.Is(err, store.ErrNotFound) for readability.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsStateConflict returns true if err is a STATE_CONFLICT StoreError.
// The scheduler calls this to detect when another instance already claimed a job.
func IsStateConflict(err error) bool {
	return errors.Is(err, ErrStateConflict)
}

// IsDuplicate returns true if err is a DUPLICATE StoreError.
// Indicates an idempotency key collision (race condition path in CreateJob).
func IsDuplicate(err error) bool {
	return errors.Is(err, ErrDuplicate)
}
