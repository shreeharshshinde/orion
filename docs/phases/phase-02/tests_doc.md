# Phase 2 Tests
## `internal/store/postgres/*_test.go`

---

## Two test suites

Phase 2 has two separate test files with different requirements:

| File | Build tag | Requires DB? | Run with |
|---|---|---|---|
| `store_unit_test.go` | none | No | `go test ./...` |
| `postgres_integration_test.go` | `integration` | Yes | `go test -tags=integration ./...` |

---

## Unit tests (`store_unit_test.go`)

These test pure Go logic with no external dependencies. They run in milliseconds.

### What they cover

**StoreError behaviour:**
```go
// errors.Is() works correctly with sentinel errors
errors.Is(store.ErrNotFound, store.ErrNotFound)       // → true
errors.Is(store.ErrNotFound, store.ErrStateConflict)  // → false
errors.Is(errors.New("other"), store.ErrNotFound)     // → false
```

**Error message formatting:**
```go
e := &StoreError{Code: "NOT_FOUND", Message: "job 123 not found"}
e.Error() // → "NOT_FOUND: job 123 not found"

e2 := &StoreError{Code: "STATE_CONFLICT"}
e2.Error() // → "STATE_CONFLICT"
```

**Convenience helpers:**
```go
store.IsNotFound(store.ErrNotFound)           // → true
store.IsNotFound(store.ErrStateConflict)      // → false
store.IsStateConflict(store.ErrStateConflict) // → true
```

**TransitionOption application:**
```go
u := &TransitionUpdateExported{}
store.WithWorkerID("worker-abc")(u)
// u.WorkerID is now *"worker-abc"
// u.StartedAt, u.ErrorMessage, etc. are still nil
```

### Run:
```bash
go test ./internal/store/... -v
```

---

## Integration tests (`postgres_integration_test.go`)

These test the actual SQL against a real PostgreSQL database. They require:

```bash
# 1. Start PostgreSQL
docker compose up -d postgres

# 2. Apply migrations (creates tables and indexes)
make migrate-up

# 3. Run integration tests
go test -tags=integration ./internal/store/postgres/... -v
```

### What they cover

#### CreateJob
- `TestCreateJob_Basic` — minimal job creation, verify all fields populated
- `TestCreateJob_DefaultsApplied` — no explicit status/priority/queue → defaults applied
- `TestCreateJob_IdempotencyKey_ReturnsExisting` — same key twice → same job_id returned, only 1 row in DB

#### GetJob
- `TestGetJob_Found` — retrieve by UUID, verify fields match
- `TestGetJob_NotFound` — unknown UUID → `store.ErrNotFound`

#### TransitionJobState (CAS)
- `TestTransitionJobState_Success` — valid transition updates status in DB
- `TestTransitionJobState_CAS_Conflict` — second transition with wrong `expectedStatus` → `ErrStateConflict`
- `TestTransitionJobState_WithOptions` — `WithWorkerID` and `WithStartedAt` set in same UPDATE
- `TestTransitionJobState_FailedIncrementsAttempt` — `attempt` column increments atomically on failure

#### MarkJob* convenience wrappers
- `TestMarkJobRunning` — `scheduled → running`, sets `worker_id` and `started_at`
- `TestMarkJobCompleted` — `running → completed`, sets `completed_at`
- `TestMarkJobFailed` — `running → failed`, sets `error_message`, `next_retry_at`, increments `attempt`

#### ListJobs
- `TestListJobs_FilterByStatus` — queued filter returns queued jobs, not scheduled
- `TestListJobs_OrderByPriority` — high priority appears before normal and low

#### ReclaimOrphanedJobs
- `TestReclaimOrphanedJobs` — job with non-existent worker_id reset to queued, worker_id cleared

#### ExecutionStore (append-only audit log)
- `TestRecordAndGetExecutions` — insert execution, retrieve with correct fields
- `TestRecordExecution_Idempotent` — ON CONFLICT DO NOTHING: second insert with same (job_id, attempt) is silently ignored, first data preserved

#### WorkerStore (heartbeat)
- `TestRegisterAndHeartbeatWorker` — register, heartbeat, verify appears in ListActiveWorkers
- `TestRegisterWorker_Idempotent` — register twice (restart simulation), ON CONFLICT updates fields
- `TestDeregisterWorker` — after deregister, worker NOT in ListActiveWorkers (status=offline excluded)

---

## Test helper functions

### `makeJob(overrides...)`

Creates a minimal valid job for testing. Each call generates a new UUID to prevent test collisions.

```go
// Basic usage
job := makeJob()

// With overrides
highPriority := makeJob(func(j *domain.Job) {
    j.Priority = domain.PriorityHigh
    j.QueueName = "high"
})

withKey := makeJob(func(j *domain.Job) {
    j.IdempotencyKey = "test-key-001"
})
```

### `cleanupJob(t, db, id)`

Deletes a job after a test. Uses `t.Cleanup` equivalent pattern — best-effort, non-fatal.

```go
created, _ := db.CreateJob(ctx, job)
defer cleanupJob(t, db, created.ID)  // clean up even if test fails
```

This uses `ON DELETE CASCADE` on `job_executions`, `pipeline_jobs` etc., so deleting a job also removes all its executions.

### `newTestStore(t)`

Creates a real `*postgres.DB` connected to the test database. Registers `pool.Close()` as a cleanup function via `t.Cleanup`.

```go
db := newTestStore(t)  // panics if DB unreachable
// db.pool is closed automatically after the test
```

---

## Running specific tests

```bash
# All unit tests (fast, no DB)
go test ./internal/store/... -v -run TestStoreError
go test ./internal/store/... -v -run TestIsNotFound
go test ./internal/store/... -v -run TestWithWorkerID

# All integration tests (requires DB)
go test -tags=integration ./internal/store/postgres/... -v

# Specific integration test
go test -tags=integration ./internal/store/postgres/... -v -run TestTransitionJobState_CAS_Conflict

# With race detector (always recommended)
go test -tags=integration -race ./internal/store/postgres/... -v

# With a different DSN
ORION_DATABASE_DSN="postgres://user:pass@host:5432/db" \
    go test -tags=integration ./internal/store/postgres/... -v
```

---

## What good test output looks like

```
=== RUN   TestCreateJob_Basic
--- PASS: TestCreateJob_Basic (0.012s)
=== RUN   TestCreateJob_IdempotencyKey_ReturnsExisting
--- PASS: TestCreateJob_IdempotencyKey_ReturnsExisting (0.008s)
=== RUN   TestTransitionJobState_CAS_Conflict
--- PASS: TestTransitionJobState_CAS_Conflict (0.006s)
=== RUN   TestTransitionJobState_FailedIncrementsAttempt
--- PASS: TestTransitionJobState_FailedIncrementsAttempt (0.009s)
=== RUN   TestReclaimOrphanedJobs
--- PASS: TestReclaimOrphanedJobs (0.011s)
=== RUN   TestRecordExecution_Idempotent
--- PASS: TestRecordExecution_Idempotent (0.007s)
=== RUN   TestRegisterAndHeartbeatWorker
--- PASS: TestRegisterAndHeartbeatWorker (0.013s)
PASS
ok  github.com/shreeharsh-a/orion/internal/store/postgres 0.089s
```

Each test is independent. Failures in one test do not affect others. Cleanup functions ensure no orphaned rows accumulate in the database.

---

## Makefile shortcuts

```makefile
# Unit tests only (no build tag needed)
make test

# Integration tests (requires docker compose up -d)
make test-integration

# Both, with race detector
make check
```

The `Makefile` already has these targets from Phase 1. The `-tags=integration` flag is added in `test-integration`:

```makefile
test-integration:
    go test ./... -v -race -tags=integration -timeout 300s
```