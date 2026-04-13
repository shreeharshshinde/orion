// Package store defines the persistence interfaces for Orion.
//
// Architecture rule: NO SQL lives here. This package only defines contracts
// (interfaces) and shared types. The actual SQL lives in store/postgres/db.go.
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
	CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error)
	GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error)
	GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error)
	TransitionJobState(ctx context.Context, id uuid.UUID, expectedStatus, newStatus domain.JobStatus, opts ...TransitionOption) error
	ListJobs(ctx context.Context, filter JobFilter) ([]*domain.Job, error)
	ClaimPendingJobs(ctx context.Context, queueName, workerID string, limit int) ([]*domain.Job, error)
	MarkJobRunning(ctx context.Context, id uuid.UUID, workerID string) error
	MarkJobCompleted(ctx context.Context, id uuid.UUID) error
	MarkJobFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt *time.Time) error
	ReclaimOrphanedJobs(ctx context.Context, staleThreshold time.Duration) (int, error)
	DeleteJob(ctx context.Context, id uuid.UUID) error
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
// Store — composite interface
// ============================================================

// Store composes all three sub-interfaces.
// postgres.DB implements this. In tests, pass a fake/mock implementation.
type Store interface {
	JobStore
	ExecutionStore
	WorkerStore
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