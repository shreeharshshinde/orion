//go:build integration
// +build integration

// Package postgres_test contains integration tests for the PostgreSQL store.
//
// These tests require a running PostgreSQL instance. They are gated behind
// the `integration` build tag so they do not run during normal `go test ./...`.
//
// Run integration tests with:
//
//	make test-integration
//	# or:
//	go test -tags=integration ./internal/store/postgres/... -v
//
// Prerequisites:
//
//	docker compose up -d postgres
//	make migrate-up
//
// The tests use the ORION_DATABASE_DSN environment variable (or the default
// local DSN). Each test creates its own isolated data and cleans up on exit.
package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shreeharsh-a/orion/internal/domain"
	"github.com/shreeharsh-a/orion/internal/store"
	"github.com/shreeharsh-a/orion/internal/store/postgres"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// testDSN returns the PostgreSQL DSN for integration tests.
// Falls back to the local docker compose DSN if no env var is set.
func testDSN() string {
	if dsn := os.Getenv("ORION_DATABASE_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://orion:orion@localhost:5432/orion?sslmode=disable"
}

// newTestStore creates a real postgres.DB connected to the test database.
// It registers a cleanup function that closes the pool after the test.
func newTestStore(t *testing.T) *postgres.DB {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("failed to connect to postgres: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("postgres ping failed (is docker compose up?): %v", err)
	}

	t.Cleanup(func() { pool.Close() })

	return postgres.New(pool)
}

// makeJob creates a minimal valid domain.Job for use in tests.
// Each call generates a new UUID so tests don't collide.
func makeJob(overrides ...func(*domain.Job)) *domain.Job {
	j := &domain.Job{
		ID:         uuid.New(),
		Name:       "test-job-" + uuid.New().String()[:8],
		Type:       domain.JobTypeInline,
		QueueName:  "default",
		Priority:   domain.PriorityNormal,
		Status:     domain.JobStatusQueued,
		MaxRetries: 3,
		Payload: domain.JobPayload{
			HandlerName: "noop",
			Args:        map[string]any{"key": "value"},
		},
	}
	for _, fn := range overrides {
		fn(j)
	}
	return j
}

// cleanupJob deletes a job after a test. Non-fatal — test cleanup is best-effort.
func cleanupJob(t *testing.T, db *postgres.DB, id uuid.UUID) {
	t.Helper()
	_ = db.DeleteJob(context.Background(), id)
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateJob tests
// ─────────────────────────────────────────────────────────────────────────────

func TestCreateJob_Basic(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, err := db.CreateJob(ctx, job)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}
	defer cleanupJob(t, db, created.ID)

	// Returned job should have all fields populated.
	if created.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}
	if created.Status != domain.JobStatusQueued {
		t.Errorf("expected status=queued, got %s", created.Status)
	}
	if created.Priority != domain.PriorityNormal {
		t.Errorf("expected priority=5, got %d", created.Priority)
	}
	if created.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestCreateJob_DefaultsApplied(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	// Submit a job with minimal fields — store should fill in defaults.
	job := &domain.Job{
		Name:    "defaults-test",
		Type:    domain.JobTypeInline,
		Payload: domain.JobPayload{HandlerName: "noop"},
	}

	created, err := db.CreateJob(ctx, job)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}
	defer cleanupJob(t, db, created.ID)

	if created.Status != domain.JobStatusQueued {
		t.Errorf("expected default status=queued, got %s", created.Status)
	}
	if created.Priority != domain.PriorityNormal {
		t.Errorf("expected default priority=5, got %d", created.Priority)
	}
	if created.QueueName != "default" {
		t.Errorf("expected default queue_name=default, got %s", created.QueueName)
	}
}

func TestCreateJob_IdempotencyKey_ReturnsExisting(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	key := "idem-test-" + uuid.New().String()
	job := makeJob(func(j *domain.Job) { j.IdempotencyKey = key })

	first, err := db.CreateJob(ctx, job)
	if err != nil {
		t.Fatalf("first CreateJob failed: %v", err)
	}
	defer cleanupJob(t, db, first.ID)

	// Second call with the same key — must return the same job.
	second, err := db.CreateJob(ctx, &domain.Job{
		Name:           "different-name", // different, should be ignored
		Type:           domain.JobTypeInline,
		Payload:        domain.JobPayload{HandlerName: "noop"},
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("second CreateJob failed: %v", err)
	}

	// Both calls must return the same job ID.
	if first.ID != second.ID {
		t.Errorf("idempotency broken: first=%s second=%s", first.ID, second.ID)
	}

	// The database should have exactly one row for this key.
	jobs, err := db.ListJobs(ctx, store.JobFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	count := 0
	for _, j := range jobs {
		if j.IdempotencyKey == key {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 job with key %s, found %d", key, count)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetJob tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGetJob_Found(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	fetched, err := db.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}

	if fetched.ID != created.ID {
		t.Errorf("ID mismatch: got %s, want %s", fetched.ID, created.ID)
	}
	if fetched.Name != created.Name {
		t.Errorf("Name mismatch: got %s, want %s", fetched.Name, created.Name)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	_, err := db.GetJob(ctx, uuid.New())
	if !store.IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TransitionJobState (CAS) tests
// ─────────────────────────────────────────────────────────────────────────────

func TestTransitionJobState_Success(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	// queued → scheduled (valid transition)
	err := db.TransitionJobState(ctx,
		created.ID,
		domain.JobStatusQueued,
		domain.JobStatusScheduled,
	)
	if err != nil {
		t.Fatalf("TransitionJobState failed: %v", err)
	}

	// Verify the status changed in PostgreSQL.
	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusScheduled {
		t.Errorf("expected status=scheduled, got %s", fetched.Status)
	}
}

func TestTransitionJobState_CAS_Conflict(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	// First transition: queued → scheduled (succeeds)
	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled)

	// Second transition: queued → scheduled again (CAS check fails — status is now scheduled)
	err := db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled)
	if !store.IsStateConflict(err) {
		t.Errorf("expected ErrStateConflict, got: %v", err)
	}
}

func TestTransitionJobState_WithOptions(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	// queued → scheduled (with worker_id)
	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled,
		store.WithWorkerID("test-worker-1"),
	)

	// scheduled → running (with worker_id + started_at)
	now := time.Now().UTC()
	err := db.TransitionJobState(ctx, created.ID, domain.JobStatusScheduled, domain.JobStatusRunning,
		store.WithWorkerID("test-worker-1"),
		store.WithStartedAt(now),
	)
	if err != nil {
		t.Fatalf("TransitionJobState to running failed: %v", err)
	}

	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusRunning {
		t.Errorf("expected status=running, got %s", fetched.Status)
	}
	if fetched.WorkerID != "test-worker-1" {
		t.Errorf("expected worker_id=test-worker-1, got %s", fetched.WorkerID)
	}
	if fetched.StartedAt == nil {
		t.Error("expected started_at to be set")
	}
}

func TestTransitionJobState_FailedIncrementsAttempt(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	// Move to running state first.
	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled)
	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusScheduled, domain.JobStatusRunning)

	// Now fail it — attempt must increment atomically.
	err := db.TransitionJobState(ctx, created.ID, domain.JobStatusRunning, domain.JobStatusFailed,
		store.WithError("test error"),
	)
	if err != nil {
		t.Fatalf("TransitionJobState to failed: %v", err)
	}

	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusFailed {
		t.Errorf("expected status=failed, got %s", fetched.Status)
	}
	if fetched.Attempt != 1 {
		t.Errorf("expected attempt=1 after first failure, got %d", fetched.Attempt)
	}
	if fetched.ErrorMessage != "test error" {
		t.Errorf("expected error_message='test error', got %q", fetched.ErrorMessage)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MarkJob* convenience wrappers
// ─────────────────────────────────────────────────────────────────────────────

func TestMarkJobRunning(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled)

	if err := db.MarkJobRunning(ctx, created.ID, "worker-123"); err != nil {
		t.Fatalf("MarkJobRunning failed: %v", err)
	}

	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusRunning {
		t.Errorf("want running, got %s", fetched.Status)
	}
	if fetched.WorkerID != "worker-123" {
		t.Errorf("want worker-123, got %s", fetched.WorkerID)
	}
	if fetched.StartedAt == nil {
		t.Error("want started_at set, got nil")
	}
}

func TestMarkJobCompleted(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled)
	_ = db.MarkJobRunning(ctx, created.ID, "worker-123")

	if err := db.MarkJobCompleted(ctx, created.ID); err != nil {
		t.Fatalf("MarkJobCompleted failed: %v", err)
	}

	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusCompleted {
		t.Errorf("want completed, got %s", fetched.Status)
	}
	if fetched.CompletedAt == nil {
		t.Error("want completed_at set, got nil")
	}
}

func TestMarkJobFailed(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled)
	_ = db.MarkJobRunning(ctx, created.ID, "worker-123")

	retryAt := time.Now().Add(30 * time.Second)
	if err := db.MarkJobFailed(ctx, created.ID, "timeout", &retryAt); err != nil {
		t.Fatalf("MarkJobFailed failed: %v", err)
	}

	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusFailed {
		t.Errorf("want failed, got %s", fetched.Status)
	}
	if fetched.ErrorMessage != "timeout" {
		t.Errorf("want error=timeout, got %q", fetched.ErrorMessage)
	}
	if fetched.NextRetryAt == nil {
		t.Error("want next_retry_at set, got nil")
	}
	if fetched.Attempt != 1 {
		t.Errorf("want attempt=1, got %d", fetched.Attempt)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ListJobs tests
// ─────────────────────────────────────────────────────────────────────────────

func TestListJobs_FilterByStatus(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	// Create two queued and one scheduled job.
	j1, _ := db.CreateJob(ctx, makeJob())
	j2, _ := db.CreateJob(ctx, makeJob())
	j3, _ := db.CreateJob(ctx, makeJob())
	defer cleanupJob(t, db, j1.ID)
	defer cleanupJob(t, db, j2.ID)
	defer cleanupJob(t, db, j3.ID)

	// Move j3 to scheduled.
	_ = db.TransitionJobState(ctx, j3.ID, domain.JobStatusQueued, domain.JobStatusScheduled)

	status := domain.JobStatusQueued
	jobs, err := db.ListJobs(ctx, store.JobFilter{Status: &status, Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}

	// We should find j1 and j2 (queued), but NOT j3 (scheduled).
	found := map[uuid.UUID]bool{}
	for _, j := range jobs {
		found[j.ID] = true
	}

	if !found[j1.ID] {
		t.Error("expected to find j1 (queued)")
	}
	if !found[j2.ID] {
		t.Error("expected to find j2 (queued)")
	}
	if found[j3.ID] {
		t.Error("j3 should NOT be listed (it is scheduled, not queued)")
	}
}

func TestListJobs_OrderByPriority(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	// Create jobs with different priorities.
	low, _ := db.CreateJob(ctx, makeJob(func(j *domain.Job) { j.Priority = domain.PriorityLow }))
	high, _ := db.CreateJob(ctx, makeJob(func(j *domain.Job) { j.Priority = domain.PriorityHigh }))
	normal, _ := db.CreateJob(ctx, makeJob(func(j *domain.Job) { j.Priority = domain.PriorityNormal }))
	defer cleanupJob(t, db, low.ID)
	defer cleanupJob(t, db, high.ID)
	defer cleanupJob(t, db, normal.ID)

	status := domain.JobStatusQueued
	jobs, _ := db.ListJobs(ctx, store.JobFilter{Status: &status, Limit: 100})

	// Find positions of our test jobs in the result.
	positions := map[uuid.UUID]int{}
	for i, j := range jobs {
		positions[j.ID] = i
	}

	// high priority must appear before normal and low.
	if pos, ok := positions[high.ID]; ok {
		if normalPos, ok2 := positions[normal.ID]; ok2 {
			if pos > normalPos {
				t.Error("high priority job should appear before normal priority job")
			}
		}
		if lowPos, ok2 := positions[low.ID]; ok2 {
			if pos > lowPos {
				t.Error("high priority job should appear before low priority job")
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ReclaimOrphanedJobs tests
// ─────────────────────────────────────────────────────────────────────────────

func TestReclaimOrphanedJobs(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	// Move job to running state with a fake worker_id (no matching worker row).
	_ = db.TransitionJobState(ctx, created.ID, domain.JobStatusQueued, domain.JobStatusScheduled,
		store.WithWorkerID("ghost-worker"),
	)
	_ = db.MarkJobRunning(ctx, created.ID, "ghost-worker")

	// "ghost-worker" has no entry in the workers table, so the job is orphaned.
	// Use a very short threshold so it triggers immediately.
	count, err := db.ReclaimOrphanedJobs(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("ReclaimOrphanedJobs failed: %v", err)
	}

	// At least our job should be reclaimed (there may be others from other tests).
	if count == 0 {
		t.Error("expected at least 1 orphaned job to be reclaimed")
	}

	// Verify our job is back to queued.
	fetched, _ := db.GetJob(ctx, created.ID)
	if fetched.Status != domain.JobStatusQueued {
		t.Errorf("expected reclaimed job to be queued, got %s", fetched.Status)
	}
	if fetched.WorkerID != "" {
		t.Errorf("expected worker_id to be cleared, got %s", fetched.WorkerID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ExecutionStore tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRecordAndGetExecutions(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID) // CASCADE deletes executions too

	now := time.Now().UTC()
	exec := &domain.JobExecution{
		JobID:     created.ID,
		Attempt:   1,
		WorkerID:  "worker-test",
		Status:    domain.JobStatusCompleted,
		StartedAt: &now,
		Error:     "",
	}

	if err := db.RecordExecution(ctx, exec); err != nil {
		t.Fatalf("RecordExecution failed: %v", err)
	}

	executions, err := db.GetExecutions(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetExecutions failed: %v", err)
	}

	if len(executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(executions))
	}
	if executions[0].Attempt != 1 {
		t.Errorf("expected attempt=1, got %d", executions[0].Attempt)
	}
	if executions[0].Status != domain.JobStatusCompleted {
		t.Errorf("expected status=completed, got %s", executions[0].Status)
	}
}

func TestRecordExecution_Idempotent(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	job := makeJob()
	created, _ := db.CreateJob(ctx, job)
	defer cleanupJob(t, db, created.ID)

	exec := &domain.JobExecution{
		JobID:    created.ID,
		Attempt:  1,
		WorkerID: "w1",
		Status:   domain.JobStatusFailed,
		Error:    "first error",
	}

	// Insert twice — second must be silently ignored (ON CONFLICT DO NOTHING).
	if err := db.RecordExecution(ctx, exec); err != nil {
		t.Fatalf("first RecordExecution failed: %v", err)
	}
	exec.Error = "second error" // try to change the error
	if err := db.RecordExecution(ctx, exec); err != nil {
		t.Fatalf("second RecordExecution failed: %v", err)
	}

	executions, _ := db.GetExecutions(ctx, created.ID)
	if len(executions) != 1 {
		t.Fatalf("expected 1 execution (idempotent), got %d", len(executions))
	}
	// First error should be preserved.
	if executions[0].Error != "first error" {
		t.Errorf("expected original error preserved, got %q", executions[0].Error)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WorkerStore tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRegisterAndHeartbeatWorker(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	workerID := "test-worker-" + uuid.New().String()[:8]
	w := &domain.Worker{
		ID:          workerID,
		Hostname:    "test-host",
		QueueNames:  []string{"orion:queue:default", "orion:queue:high"},
		Concurrency: 5,
	}

	// Register.
	if err := db.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("RegisterWorker failed: %v", err)
	}
	defer func() { _ = db.DeregisterWorker(ctx, workerID) }()

	// Heartbeat.
	if err := db.Heartbeat(ctx, workerID); err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	// Should appear in ListActiveWorkers with generous TTL.
	workers, err := db.ListActiveWorkers(ctx, 60*time.Second)
	if err != nil {
		t.Fatalf("ListActiveWorkers failed: %v", err)
	}

	found := false
	for _, fw := range workers {
		if fw.ID == workerID {
			found = true
			if fw.Concurrency != 5 {
				t.Errorf("expected concurrency=5, got %d", fw.Concurrency)
			}
		}
	}
	if !found {
		t.Errorf("worker %s not found in active workers", workerID)
	}
}

func TestRegisterWorker_Idempotent(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	workerID := "test-worker-idem-" + uuid.New().String()[:8]
	w := &domain.Worker{
		ID:          workerID,
		Hostname:    "host-1",
		QueueNames:  []string{"orion:queue:default"},
		Concurrency: 3,
	}

	// Register twice (simulates restart). Must not fail.
	if err := db.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("first RegisterWorker failed: %v", err)
	}
	w.Concurrency = 10 // changed on restart
	if err := db.RegisterWorker(ctx, w); err != nil {
		t.Fatalf("second RegisterWorker (restart) failed: %v", err)
	}
	defer func() { _ = db.DeregisterWorker(ctx, workerID) }()

	workers, _ := db.ListActiveWorkers(ctx, 60*time.Second)
	for _, fw := range workers {
		if fw.ID == workerID {
			if fw.Concurrency != 10 {
				t.Errorf("expected updated concurrency=10 after restart, got %d", fw.Concurrency)
			}
		}
	}
}

func TestDeregisterWorker(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	workerID := "test-worker-dereg-" + uuid.New().String()[:8]
	w := &domain.Worker{
		ID:          workerID,
		Hostname:    "host-1",
		QueueNames:  []string{"orion:queue:default"},
		Concurrency: 1,
	}

	_ = db.RegisterWorker(ctx, w)

	if err := db.DeregisterWorker(ctx, workerID); err != nil {
		t.Fatalf("DeregisterWorker failed: %v", err)
	}

	// After deregister, worker should NOT appear in ListActiveWorkers
	// (status is 'offline', which is excluded).
	workers, _ := db.ListActiveWorkers(ctx, 60*time.Second)
	for _, fw := range workers {
		if fw.ID == workerID {
			t.Error("deregistered worker should not appear in active workers")
		}
	}
}
