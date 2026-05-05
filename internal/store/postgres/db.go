// Package postgres implements the store.Store interface using PostgreSQL via pgx/v5.
//
// This is the ONLY place in the entire Orion codebase where SQL is written.
// All other packages (scheduler, worker, API handler) call the store.Store
// interface and never know that PostgreSQL is involved.
//
// Key patterns used here:
//   - CAS (Compare-And-Swap) via UPDATE WHERE status = expected RETURNING id
//   - Dynamic SET clauses built with TransitionOption functional options
//   - pgxpool for connection pooling (never open/close connections manually)
//   - Nullable columns scanned into *string / *time.Time pointers
//   - JSONB payload serialized/deserialized with encoding/json
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// DB wraps a pgxpool.Pool and implements store.Store.
// pgxpool manages a pool of PostgreSQL connections transparently.
// We never open or close connections manually — we call pool methods
// and pgxpool handles acquiring/releasing connections from the pool.
type DB struct {
	pool *pgxpool.Pool
}

// New creates a DB from an already-dialed connection pool.
// The caller is responsible for creating the pool via pgxpool.New() or
// pgxpool.NewWithConfig() and for calling pool.Close() on shutdown.
func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool}
}

// ============================================================
// JobStore implementation
// ============================================================

// CreateJob inserts a new job into PostgreSQL.
//
// Idempotency handling (three cases):
//  1. No idempotency_key → straight INSERT, no conflict possible.
//  2. Key exists, SELECT finds it → return existing job with no INSERT attempted.
//  3. Key not found in SELECT, INSERT fails with 23505 (race: two requests hit
//     simultaneously) → SELECT again and return the winner's row.
//
// The HTTP handler returns HTTP 200 for cases 2 and 3, HTTP 201 for case 1.
func (db *DB) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
	// Case 2: check for existing idempotency key before attempting INSERT.
	if job.IdempotencyKey != "" {
		existing, err := db.GetJobByIdempotencyKey(ctx, job.IdempotencyKey)
		if err == nil {
			return existing, nil // return the existing job transparently
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("checking idempotency key: %w", err)
		}
		// ErrNotFound → key is new → proceed with INSERT
	}

	// Generate a UUID in Go so we can return it without a RETURNING clause.
	// PostgreSQL's DEFAULT gen_random_uuid() would work too, but we'd need
	// RETURNING id to get it back, adding a round-trip.
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	now := time.Now().UTC()
	job.CreatedAt = now
	job.UpdatedAt = now

	// Apply defaults so callers don't have to set them explicitly.
	if job.Status == "" {
		job.Status = domain.JobStatusQueued
	}
	if job.Priority == 0 {
		job.Priority = domain.PriorityNormal
	}
	if job.QueueName == "" {
		job.QueueName = "default"
	}

	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload: %w", err)
	}

	const q = `
		INSERT INTO jobs (
			id, idempotency_key, name, type, queue_name, priority,
			status, payload, max_retries, attempt,
			scheduled_at, deadline, created_at, updated_at
		) VALUES (
			$1,  $2,  $3,  $4,  $5,  $6,
			$7,  $8,  $9,  $10,
			$11, $12, $13, $14
		)`

	_, err = db.pool.Exec(ctx, q,
		job.ID,
		nullableString(job.IdempotencyKey),
		job.Name,
		string(job.Type),
		job.QueueName,
		int16(job.Priority),
		string(job.Status),
		payloadJSON,
		job.MaxRetries,
		job.Attempt,
		job.ScheduledAt, // *time.Time — pgx stores nil as NULL
		job.Deadline,
		job.CreatedAt,
		job.UpdatedAt,
	)
	if err != nil {
		// Case 3: concurrent INSERT with same idempotency key.
		// The UNIQUE constraint on idempotency_key fires with error code 23505.
		if isUniqueViolation(err) && job.IdempotencyKey != "" {
			existing, fetchErr := db.GetJobByIdempotencyKey(ctx, job.IdempotencyKey)
			if fetchErr == nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("inserting job: %w", err)
	}

	return job, nil
}

// GetJob retrieves a single job by its UUID primary key.
// Returns store.ErrNotFound if no row exists with the given ID.
func (db *DB) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM jobs WHERE id = $1`
	row := db.pool.QueryRow(ctx, q, id)
	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("getting job %s: %w", id, err)
	}
	return job, nil
}

// GetJobByIdempotencyKey looks up a job by client-provided idempotency key.
// Returns store.ErrNotFound if the key has never been used.
func (db *DB) GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM jobs WHERE idempotency_key = $1`
	row := db.pool.QueryRow(ctx, q, key)
	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("getting job by idempotency key: %w", err)
	}
	return job, nil
}

// TransitionJobState performs the CAS (Compare-And-Swap) state transition.
//
// The core SQL:
//
//	UPDATE jobs
//	SET status = $newStatus [, worker_id = ...] [, started_at = ...] [, ...]
//	WHERE id = $id AND status = $expectedStatus   ← CAS guard
//	RETURNING id
//
// If RETURNING id returns zero rows, the job's current status did not match
// expectedStatus — another process already transitioned it. This is a normal
// concurrent condition (two schedulers racing), not a bug. We return
// store.ErrStateConflict so the caller can skip and move on.
//
// The SET clause is built dynamically from TransitionOption functions, letting
// callers atomically update worker_id, timestamps, and error info in one round-trip.
func (db *DB) TransitionJobState(
	ctx context.Context,
	id uuid.UUID,
	expectedStatus, newStatus domain.JobStatus,
	opts ...store.TransitionOption,
) error {
	// Collect all optional field updates from the caller's TransitionOption funcs.
	u := &store.TransitionUpdateExported{}
	for _, opt := range opts {
		opt(u)
	}

	// Build the SET clause. Always update status.
	// Additional fields are added only when the caller provided a WithX option.
	setClauses := []string{"status = $3"}
	args := []any{id, string(expectedStatus), string(newStatus)}
	argIdx := 4 // next $N placeholder index

	if u.WorkerID != nil {
		setClauses = append(setClauses, fmt.Sprintf("worker_id = $%d", argIdx))
		args = append(args, *u.WorkerID)
		argIdx++
	}
	if u.ErrorMessage != nil {
		setClauses = append(setClauses, fmt.Sprintf("error_message = $%d", argIdx))
		args = append(args, *u.ErrorMessage)
		argIdx++
	}
	if u.NextRetryAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("next_retry_at = $%d", argIdx))
		args = append(args, *u.NextRetryAt)
		argIdx++
	}
	if u.StartedAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("started_at = $%d", argIdx))
		args = append(args, *u.StartedAt)
		argIdx++
	}
	if u.CompletedAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("completed_at = $%d", argIdx))
		args = append(args, *u.CompletedAt)
		argIdx++
	}

	// When failing a job, increment attempt atomically in the same UPDATE.
	// This prevents a separate UPDATE from racing with a concurrent read.
	if newStatus == domain.JobStatusFailed {
		setClauses = append(setClauses, "attempt = attempt + 1")
	}

	q := fmt.Sprintf(`
		UPDATE jobs
		SET %s
		WHERE id = $1 AND status = $2
		RETURNING id`,
		strings.Join(setClauses, ", "),
	)

	var returnedID uuid.UUID
	err := db.pool.QueryRow(ctx, q, args...).Scan(&returnedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// CAS failed: the job's status was already changed by another process.
			// This is not an error — it is the expected concurrent-access behavior.
			return store.ErrStateConflict
		}
		return fmt.Errorf("transitioning job %s %s→%s: %w", id, expectedStatus, newStatus, err)
	}
	return nil
}

// ListJobs returns jobs matching the filter, ordered by priority DESC, created_at ASC.
// This is the same ordering the scheduler uses for its dispatch loop.
func (db *DB) ListJobs(ctx context.Context, filter store.JobFilter) ([]*domain.Job, error) {
	// Build WHERE clause from whichever filter fields are non-nil.
	// "WHERE 1=1" is a safe base so every subsequent AND is valid SQL.
	whereClauses := []string{"1=1"}
	args := []any{}
	argIdx := 1

	if filter.Status != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, string(*filter.Status))
		argIdx++
	}
	if filter.QueueName != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("queue_name = $%d", argIdx))
		args = append(args, *filter.QueueName)
		argIdx++
	}
	if filter.WorkerID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("worker_id = $%d", argIdx))
		args = append(args, *filter.WorkerID)
		argIdx++
	}

	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	q := fmt.Sprintf(`
		SELECT %s FROM jobs
		WHERE %s
		ORDER BY priority DESC, created_at ASC
		LIMIT %d OFFSET %d`,
		jobColumns,
		strings.Join(whereClauses, " AND "),
		limit, offset,
	)

	rows, err := db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*domain.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning job row: %w", err)
		}
		jobs = append(jobs, job)
	}
	// Always check rows.Err() — an error during iteration is not surfaced by Next().
	return jobs, rows.Err()
}

// ClaimPendingJobs atomically claims up to `limit` queued jobs for a worker.
//
// Uses SELECT FOR UPDATE SKIP LOCKED — a PostgreSQL pattern for work distribution:
//   - FOR UPDATE: locks the selected rows so other transactions cannot modify them
//   - SKIP LOCKED: instead of waiting for locks, skip already-locked rows
//
// Result: concurrent workers each get a unique, non-overlapping set of jobs.
// No job is ever claimed by two workers, and no worker blocks another.
func (db *DB) ClaimPendingJobs(ctx context.Context, queueName, workerID string, limit int) ([]*domain.Job, error) {
	const q = `
		WITH claimed AS (
			SELECT id FROM jobs
			WHERE status = 'queued' AND queue_name = $1
			ORDER BY priority DESC, created_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs
		SET status = 'scheduled', worker_id = $3
		FROM claimed
		WHERE jobs.id = claimed.id
		RETURNING ` + jobColumns

	rows, err := db.pool.Query(ctx, q, queueName, limit, workerID)
	if err != nil {
		return nil, fmt.Errorf("claiming pending jobs for queue %s: %w", queueName, err)
	}
	defer rows.Close()

	var jobs []*domain.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning claimed job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// MarkJobRunning transitions a job from scheduled → running.
// Sets worker_id and started_at atomically in the same UPDATE.
// Returns store.ErrStateConflict if the job was already transitioned by
// another worker (safe to retry on a different job).
func (db *DB) MarkJobRunning(ctx context.Context, id uuid.UUID, workerID string) error {
	now := time.Now().UTC()
	return db.TransitionJobState(ctx, id,
		domain.JobStatusScheduled,
		domain.JobStatusRunning,
		store.WithWorkerID(workerID),
		store.WithStartedAt(now),
	)
}

// MarkJobCompleted transitions a job from running → completed.
// Sets completed_at atomically. Called by the worker after executor.Execute()
// returns nil and BEFORE calling ackFn(nil) to remove from Redis PEL.
func (db *DB) MarkJobCompleted(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	return db.TransitionJobState(ctx, id,
		domain.JobStatusRunning,
		domain.JobStatusCompleted,
		store.WithCompletedAt(now),
	)
}

// MarkJobFailed transitions a job from running → failed.
// Increments attempt atomically (handled inside TransitionJobState when
// newStatus == JobStatusFailed). Sets error_message and next_retry_at.
// Called by the worker after executor.Execute() returns a non-nil error.
func (db *DB) MarkJobFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt *time.Time) error {
	opts := []store.TransitionOption{store.WithError(errMsg)}
	if nextRetryAt != nil {
		opts = append(opts, store.WithNextRetryAt(*nextRetryAt))
	}
	return db.TransitionJobState(ctx, id,
		domain.JobStatusRunning,
		domain.JobStatusFailed,
		opts...,
	)
}

// ReclaimOrphanedJobs finds jobs in 'running' state whose worker has gone silent
// (last_heartbeat older than staleThreshold) and resets them to 'queued'.
//
// This is the crash-recovery mechanism. Workers heartbeat every 15 seconds.
// staleThreshold is typically workerHeartbeatTTL * 2 = 90 seconds.
// Any job whose worker missed ~6 heartbeats is considered orphaned.
//
// The scheduler calls this every 30 seconds. Returns the count of reclaimed jobs.
func (db *DB) ReclaimOrphanedJobs(ctx context.Context, staleThreshold time.Duration) (int, error) {
	// The CTE finds running jobs whose worker either:
	//   a) has no worker_id recorded (defensive)
	//   b) has a last_heartbeat older than the threshold
	//   c) has a worker_id that doesn't exist in the workers table at all
	//
	// The UPDATE resets them to queued so the scheduler can redispatch them.
	const q = `
		WITH orphaned AS (
			SELECT j.id FROM jobs j
			WHERE j.status = 'running'
			AND (
				j.worker_id IS NULL
				OR NOT EXISTS (
					SELECT 1 FROM workers w
					WHERE w.id = j.worker_id
					AND w.last_heartbeat > NOW() - $1::interval
				)
			)
		)
		UPDATE jobs
		SET
			status        = 'queued',
			worker_id     = NULL,
			error_message = 'reclaimed: worker went silent'
		FROM orphaned
		WHERE jobs.id = orphaned.id
		RETURNING jobs.id`

	// PostgreSQL interval from Go duration: e.g., "90s"
	intervalStr := fmt.Sprintf("%ds", int(staleThreshold.Seconds()))
	rows, err := db.pool.Query(ctx, q, intervalStr)
	if err != nil {
		return 0, fmt.Errorf("reclaiming orphaned jobs: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return count, fmt.Errorf("scanning reclaimed id: %w", err)
		}
		count++
	}
	return count, rows.Err()
}

// DeleteJob hard-deletes a job by ID. Admin use only.
// Prefer transitioning to 'cancelled' status — this preserves the audit trail
// in job_executions. Hard delete removes everything permanently.
func (db *DB) DeleteJob(ctx context.Context, id uuid.UUID) error {
	result, err := db.pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting job %s: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ============================================================
// ExecutionStore implementation
// ============================================================

// RecordExecution inserts a new execution attempt into job_executions.
//
// INVARIANT: this table is append-only. Never call UPDATE on it.
// Each attempt gets its own immutable row. Three failed attempts + one
// success = four rows with attempt numbers 1, 2, 3, 4.
//
// ON CONFLICT (job_id, attempt) DO NOTHING makes this idempotent — if the
// worker retries RecordExecution itself (e.g., due to a transient error),
// the second call is silently ignored.
func (db *DB) RecordExecution(ctx context.Context, exec *domain.JobExecution) error {
	if exec.ID == uuid.Nil {
		exec.ID = uuid.New()
	}
	exec.CreatedAt = time.Now().UTC()

	const q = `
		INSERT INTO job_executions (
			id, job_id, attempt, worker_id, status,
			started_at, finished_at, exit_code, logs_ref, error, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11
		)
		ON CONFLICT (job_id, attempt) DO NOTHING`

	_, err := db.pool.Exec(ctx, q,
		exec.ID,
		exec.JobID,
		exec.Attempt,
		exec.WorkerID,
		string(exec.Status),
		exec.StartedAt,
		exec.FinishedAt,
		exec.ExitCode,
		nullableString(exec.LogsRef),
		nullableString(exec.Error),
		exec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("recording execution for job %s attempt %d: %w", exec.JobID, exec.Attempt, err)
	}
	return nil
}

// GetExecutions returns all execution attempts for a job, ordered by attempt ASC.
// Returns an empty (non-nil) slice if no executions exist yet.
func (db *DB) GetExecutions(ctx context.Context, jobID uuid.UUID) ([]*domain.JobExecution, error) {
	const q = `
		SELECT
			id, job_id, attempt, worker_id, status,
			started_at, finished_at, exit_code, logs_ref, error, created_at
		FROM job_executions
		WHERE job_id = $1
		ORDER BY attempt ASC`

	rows, err := db.pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("getting executions for job %s: %w", jobID, err)
	}
	defer rows.Close()

	var execs []*domain.JobExecution
	for rows.Next() {
		e := &domain.JobExecution{}
		var (
			statusStr string
			logsRef   *string
			errStr    *string
		)
		if err := rows.Scan(
			&e.ID, &e.JobID, &e.Attempt, &e.WorkerID, &statusStr,
			&e.StartedAt, &e.FinishedAt, &e.ExitCode, &logsRef, &errStr, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning execution row: %w", err)
		}
		e.Status = domain.JobStatus(statusStr)
		if logsRef != nil {
			e.LogsRef = *logsRef
		}
		if errStr != nil {
			e.Error = *errStr
		}
		execs = append(execs, e)
	}
	if execs == nil {
		execs = []*domain.JobExecution{} // return empty slice, not nil
	}
	return execs, rows.Err()
}

// ============================================================
// WorkerStore implementation
// ============================================================

// RegisterWorker upserts a worker record on startup.
//
// ON CONFLICT (id) DO UPDATE handles the restart case: if the same worker
// (identified by hostname or ORION_WORKER_ID) restarts, we update its
// registration instead of returning a duplicate key error.
//
// active_jobs is always reset to 0 on register — a restarting worker
// has no in-flight jobs until it dequeues and starts executing.
func (db *DB) RegisterWorker(ctx context.Context, w *domain.Worker) error {
	w.LastHeartbeat = time.Now().UTC()
	if w.RegisteredAt.IsZero() {
		w.RegisteredAt = w.LastHeartbeat
	}

	const q = `
		INSERT INTO workers (
			id, hostname, queue_names, concurrency,
			active_jobs, status, last_heartbeat, registered_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			hostname       = EXCLUDED.hostname,
			queue_names    = EXCLUDED.queue_names,
			concurrency    = EXCLUDED.concurrency,
			active_jobs    = 0,
			status         = 'idle',
			last_heartbeat = EXCLUDED.last_heartbeat`

	_, err := db.pool.Exec(ctx, q,
		w.ID,
		w.Hostname,
		w.QueueNames, // pgx natively handles []string → PostgreSQL TEXT[]
		w.Concurrency,
		0, // active_jobs always 0 on register
		string(domain.WorkerStatusIdle),
		w.LastHeartbeat,
		w.RegisteredAt,
	)
	if err != nil {
		return fmt.Errorf("registering worker %s: %w", w.ID, err)
	}
	return nil
}

// Heartbeat updates last_heartbeat to NOW() for the given worker.
// Called every 15 seconds by the worker's heartbeat goroutine.
// The scheduler uses last_heartbeat to distinguish alive workers from crashed ones.
func (db *DB) Heartbeat(ctx context.Context, workerID string) error {
	result, err := db.pool.Exec(ctx,
		`UPDATE workers SET last_heartbeat = NOW() WHERE id = $1`,
		workerID,
	)
	if err != nil {
		return fmt.Errorf("heartbeat for worker %s: %w", workerID, err)
	}
	if result.RowsAffected() == 0 {
		// Worker record disappeared — should not happen, but handle it gracefully.
		return store.ErrNotFound
	}
	return nil
}

// ListActiveWorkers returns workers whose last_heartbeat is within the TTL window.
// Workers with last_heartbeat older than ttl are considered crashed or unreachable.
func (db *DB) ListActiveWorkers(ctx context.Context, ttl time.Duration) ([]*domain.Worker, error) {
	intervalStr := fmt.Sprintf("%ds", int(ttl.Seconds()))
	const q = `
		SELECT
			id, hostname, queue_names, concurrency,
			active_jobs, status, last_heartbeat, registered_at
		FROM workers
		WHERE last_heartbeat > NOW() - $1::interval
		  AND status != 'offline'
		ORDER BY registered_at ASC`

	rows, err := db.pool.Query(ctx, q, intervalStr)
	if err != nil {
		return nil, fmt.Errorf("listing active workers: %w", err)
	}
	defer rows.Close()

	var workers []*domain.Worker
	for rows.Next() {
		w := &domain.Worker{}
		var statusStr string
		if err := rows.Scan(
			&w.ID, &w.Hostname, &w.QueueNames, &w.Concurrency,
			&w.ActiveJobs, &statusStr, &w.LastHeartbeat, &w.RegisteredAt,
		); err != nil {
			return nil, fmt.Errorf("scanning worker row: %w", err)
		}
		w.Status = domain.WorkerStatus(statusStr)
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

// DeregisterWorker marks a worker as offline on clean shutdown.
// This is called when the worker pool drains and exits after receiving SIGTERM.
// Setting status='offline' immediately signals the scheduler that this worker
// is gone — it doesn't have to wait for the 90-second orphan detection timeout.
func (db *DB) DeregisterWorker(ctx context.Context, workerID string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE workers SET status = 'offline', last_heartbeat = NOW() WHERE id = $1`,
		workerID,
	)
	if err != nil {
		return fmt.Errorf("deregistering worker %s: %w", workerID, err)
	}
	return nil
}

// ============================================================
// Scanning helpers
// ============================================================

// jobColumns is the canonical SELECT column list for the jobs table.
// The column ORDER here MUST exactly match the Scan() call in scanJob().
// If you add a column here, add it to scanJob() in the same position.
const jobColumns = `
	id, idempotency_key, name, type, queue_name, priority,
	status, payload, max_retries, attempt,
	worker_id, error_message,
	scheduled_at, deadline, next_retry_at, started_at, completed_at,
	created_at, updated_at`

// pgxScanner is satisfied by both pgx.Row (single row) and pgx.Rows (multi-row).
// This lets scanJob() work for both QueryRow() and Query() results.
type pgxScanner interface {
	Scan(dest ...any) error
}

// scanJob reads one row from the jobs table into a domain.Job.
// Column order must exactly match jobColumns above.
func scanJob(row pgxScanner) (*domain.Job, error) {
	j := &domain.Job{}

	// Intermediate variables for columns that need type conversion:
	//   *string  — for nullable VARCHAR columns (NULL → nil pointer)
	//   string   — for enum columns stored as text (convert to typed constant after scan)
	//   []byte   — for JSONB columns (unmarshal after scan)
	//   int16    — for SMALLINT priority (convert to domain.JobPriority after scan)
	var (
		idempotencyKey *string
		typeStr        string
		statusStr      string
		payloadJSON    []byte
		workerID       *string
		errorMessage   *string
		priority       int16
	)

	err := row.Scan(
		&j.ID,
		&idempotencyKey, // *string: NULL → nil, non-NULL → *"key"
		&j.Name,
		&typeStr,
		&j.QueueName,
		&priority,
		&statusStr,
		&payloadJSON,
		&j.MaxRetries,
		&j.Attempt,
		&workerID,      // *string
		&errorMessage,  // *string
		&j.ScheduledAt, // *time.Time: pgx handles NULL automatically
		&j.Deadline,
		&j.NextRetryAt,
		&j.StartedAt,
		&j.CompletedAt,
		&j.CreatedAt,
		&j.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Convert string columns to their typed Go constants.
	j.Type = domain.JobType(typeStr)
	j.Status = domain.JobStatus(statusStr)
	j.Priority = domain.JobPriority(priority)

	// Dereference nullable string pointers.
	// We use *string for nullable VARCHAR so pgx doesn't return an error on NULL.
	if idempotencyKey != nil {
		j.IdempotencyKey = *idempotencyKey
	}
	if workerID != nil {
		j.WorkerID = *workerID
	}
	if errorMessage != nil {
		j.ErrorMessage = *errorMessage
	}

	// Unmarshal JSONB payload into domain.JobPayload struct.
	if len(payloadJSON) > 0 {
		if err := json.Unmarshal(payloadJSON, &j.Payload); err != nil {
			return nil, fmt.Errorf("unmarshaling payload: %w", err)
		}
	}

	return j, nil
}

// ============================================================
// Utility helpers
// ============================================================

// nullableString converts an empty Go string to nil so pgx stores NULL
// in the database rather than an empty string "". This matters for
// idempotency_key which has a UNIQUE constraint — only one NULL value
// is allowed under SQL standard semantics, but multiple NULLs are fine
// because NULL != NULL in SQL.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isUniqueViolation returns true if err is a PostgreSQL unique constraint
// violation (error code 23505). We check the error message string because
// pgx wraps the error and the concrete type may vary.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "unique_violation") ||
		strings.Contains(msg, "duplicate key")
}

// ============================================================
// QueueConfigStore implementation — Phase 8
// ============================================================

// ListQueueConfigs returns all rows from queue_config, ordered by queue_name.
// Called by GET /queues and by the scheduler on each tick for live-reload.
func (db *DB) ListQueueConfigs(ctx context.Context) ([]*store.QueueConfig, error) {
	const q = `
		SELECT queue_name, max_concurrent, weight, rate_per_sec, burst, enabled, updated_at
		FROM queue_config
		ORDER BY queue_name ASC`

	rows, err := db.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing queue configs: %w", err)
	}
	defer rows.Close()

	var configs []*store.QueueConfig
	for rows.Next() {
		c := &store.QueueConfig{}
		if err := rows.Scan(
			&c.QueueName, &c.MaxConcurrent, &c.Weight,
			&c.RatePerSec, &c.Burst, &c.Enabled, &c.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning queue config row: %w", err)
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// GetQueueConfig returns the configuration for a single queue by name.
// Returns store.ErrNotFound if the queue_name does not exist.
func (db *DB) GetQueueConfig(ctx context.Context, queueName string) (*store.QueueConfig, error) {
	const q = `
		SELECT queue_name, max_concurrent, weight, rate_per_sec, burst, enabled, updated_at
		FROM queue_config
		WHERE queue_name = $1`

	c := &store.QueueConfig{}
	err := db.pool.QueryRow(ctx, q, queueName).Scan(
		&c.QueueName, &c.MaxConcurrent, &c.Weight,
		&c.RatePerSec, &c.Burst, &c.Enabled, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &store.StoreError{Code: "NOT_FOUND",
				Message: "queue config not found: " + queueName}
		}
		return nil, fmt.Errorf("getting queue config %s: %w", queueName, err)
	}
	return c, nil
}

// UpsertQueueConfig creates or updates a queue configuration row.
// Sets updated_at = NOW() automatically. Returns the updated row.
func (db *DB) UpsertQueueConfig(ctx context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) {
	const q = `
		INSERT INTO queue_config (queue_name, max_concurrent, weight, rate_per_sec, burst, enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (queue_name) DO UPDATE SET
			max_concurrent = EXCLUDED.max_concurrent,
			weight         = EXCLUDED.weight,
			rate_per_sec   = EXCLUDED.rate_per_sec,
			burst          = EXCLUDED.burst,
			enabled        = EXCLUDED.enabled,
			updated_at     = NOW()
		RETURNING queue_name, max_concurrent, weight, rate_per_sec, burst, enabled, updated_at`

	updated := &store.QueueConfig{}
	err := db.pool.QueryRow(ctx, q,
		cfg.QueueName, cfg.MaxConcurrent, cfg.Weight,
		cfg.RatePerSec, cfg.Burst, cfg.Enabled,
	).Scan(
		&updated.QueueName, &updated.MaxConcurrent, &updated.Weight,
		&updated.RatePerSec, &updated.Burst, &updated.Enabled, &updated.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upserting queue config %s: %w", cfg.QueueName, err)
	}
	return updated, nil
}
