# Step 04 — Store Interface (`internal/store/store.go`)

## What is this file?

This file defines the **contract** for all database operations. It does NOT contain any actual database code — no SQL, no PostgreSQL, nothing. It just describes *what operations are possible*.

This is Go's **interface pattern**: define what you need, implement it separately, swap the implementation without touching the callers.

```
store.go        ← defines the INTERFACE (what)     ← you are here
store/postgres/ ← implements the interface (how)   ← Phase 2
```

## Why interfaces instead of just writing SQL directly?

**Without interface:**
```go
// Worker pool calls PostgreSQL directly
func (p *Pool) markRunning(jobID uuid.UUID) {
    db.Exec("UPDATE jobs SET status='running' WHERE id=$1", jobID)
}
```
Problem: To test this, you need a real PostgreSQL database running. Tests are slow and fragile.

**With interface:**
```go
// Worker pool calls the interface
func (p *Pool) markRunning(jobID uuid.UUID) {
    p.store.MarkJobRunning(ctx, jobID, p.cfg.WorkerID)
}
```
Now in tests, you pass a fake `store` that returns whatever you want — no database needed. Tests run in milliseconds.

## The three interfaces

### `JobStore` — everything about jobs
```go
type JobStore interface {
    CreateJob(ctx, job) (*Job, error)
    GetJob(ctx, id) (*Job, error)
    TransitionJobState(ctx, id, expectedStatus, newStatus, opts...) error
    ListJobs(ctx, filter) ([]*Job, error)
    ClaimPendingJobs(ctx, queueName, workerID, limit) ([]*Job, error)
    MarkJobRunning(ctx, id, workerID) error
    MarkJobCompleted(ctx, id) error
    MarkJobFailed(ctx, id, errMsg, nextRetryAt) error
    ReclaimOrphanedJobs(ctx, staleThreshold) (int, error)
    DeleteJob(ctx, id) error
}
```

**The most important method: `TransitionJobState`**

This is where **CAS (Compare-And-Swap)** lives:

```go
// Internally, this runs:
// UPDATE jobs SET status = $new_state WHERE id = $id AND status = $expected_state
// RETURNING id;
//
// If 0 rows returned → someone else already changed the status → ErrStateConflict
TransitionJobState(ctx, jobID, JobStatusQueued, JobStatusScheduled)
```

This prevents two schedulers from both scheduling the same job. Only the first one succeeds; the second gets `ErrStateConflict` and skips it.

### `ExecutionStore` — append-only audit log
```go
type ExecutionStore interface {
    RecordExecution(ctx, exec *JobExecution) error  // INSERT only, never UPDATE
    GetExecutions(ctx, jobID) ([]*JobExecution, error)
}
```

Every time a worker runs a job, we INSERT a new row into `job_executions`. We never update or delete it. If a job is retried 3 times, there are 3 rows — one per attempt. This gives you a complete history of what happened.

### `WorkerStore` — worker heartbeats
```go
type WorkerStore interface {
    RegisterWorker(ctx, worker) error     // called on startup
    Heartbeat(ctx, workerID) error        // called every 15s
    ListActiveWorkers(ctx, ttl) ([]*Worker, error)
    DeregisterWorker(ctx, workerID) error // called on clean shutdown
}
```

Workers tell the system they're alive by calling `Heartbeat()` every 15 seconds. The scheduler checks `ListActiveWorkers()` to find which workers have gone silent and need their jobs reclaimed.

### `Store` — the full interface
```go
type Store interface {
    JobStore
    ExecutionStore
    WorkerStore
}
```

This composes all three interfaces into one. The PostgreSQL implementation (`postgres.DB`) implements all of them in one struct.

## Sentinel errors

```go
var (
    ErrNotFound      = &StoreError{Code: "NOT_FOUND"}
    ErrStateConflict = &StoreError{Code: "STATE_CONFLICT"}  // CAS failed
    ErrDuplicate     = &StoreError{Code: "DUPLICATE"}        // idempotency key already exists
)
```

These let callers check the specific type of error:
```go
err := store.TransitionJobState(...)
if errors.Is(err, store.ErrStateConflict) {
    // Another scheduler got to it first — skip this job, not an error
    continue
}
```

## TransitionOption — the functional options pattern

Some state transitions need extra fields set at the same time:
```go
// When marking a job as running, also set worker_id and started_at
store.TransitionJobState(ctx, id, queued, running,
    store.WithWorkerID("worker-prod-3"),
)

// When marking a job as failed, also set error_message and next_retry_at
store.TransitionJobState(ctx, id, running, failed,
    store.WithError("OOM killed"),
    store.WithNextRetryAt(time.Now().Add(30*time.Second)),
)
```

This **functional options** pattern lets you add optional fields without creating 10 different method signatures.

## JobFilter — for ListJobs

```go
type JobFilter struct {
    Status    *domain.JobStatus  // filter by status (nil = all)
    QueueName *string            // filter by queue (nil = all)
    WorkerID  *string            // filter by worker (nil = all)
    Limit     int                // max rows to return
    Offset    int                // for pagination
}
```

Using pointers (`*string`) allows distinguishing "not filtered" (nil pointer) from "filter by empty string" (pointer to "").

## File location
```
orion/
└── internal/
    └── store/
        ├── store.go         ← you are here (interface)
        └── postgres/        ← Phase 2 (implementation, not yet built)
            └── db.go
```

## Next step
Now we define the **Queue interface** — how jobs move from the scheduler into workers via Redis.