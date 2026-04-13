# `internal/store/postgres/db.go`
## The PostgreSQL Store Implementation

---

## What this file is

This is the **only place in the entire Orion codebase where SQL is written**.

Every other package — the scheduler, worker pool, API handler — calls the `store.Store` interface and has no idea that PostgreSQL is involved. If you wanted to swap PostgreSQL for CockroachDB, MySQL, or even an in-memory fake, you'd only change this one file (and maybe `go.mod`).

```
store/store.go        ← interface (what operations exist)
store/postgres/db.go  ← implementation (how they work in PostgreSQL) ← YOU ARE HERE
```

---

## The DB struct

```go
type DB struct {
    pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *DB {
    return &DB{pool: pool}
}
```

Just a wrapper around `pgxpool.Pool`. All PostgreSQL state lives in the database, not in this struct. This makes `DB` safe to share across goroutines — the pool handles concurrent access internally.

---

## How the pool is created (in main.go)

```go
poolCfg, _ := pgxpool.ParseConfig(cfg.Database.DSN)
poolCfg.MaxConns = cfg.Database.MaxConns          // 20 — max simultaneous connections
poolCfg.MinConns = cfg.Database.MinConns          // 2  — keep at least 2 warm
poolCfg.MaxConnIdleTime = cfg.Database.MaxConnIdleTime  // close idle after 10min
poolCfg.MaxConnLifetime = cfg.Database.MaxConnLifetime  // recycle after 60min

db, err := pgxpool.NewWithConfig(ctx, poolCfg)
if err != nil {
    logger.Error("failed to connect to postgres", "err", err)
    os.Exit(1)
}
if err := db.Ping(ctx); err != nil {
    logger.Error("postgres ping failed", "err", err)
    os.Exit(1)
}

pgStore := postgres.New(db)
```

Why `pgxpool.NewWithConfig` over `pgxpool.New`? The config version lets you set connection limits. Without `MaxConns`, the pool could open unlimited connections and exhaust PostgreSQL's `max_connections` under load.

---

## Three pool usage patterns

Every method uses exactly one of these three patterns:

### Pattern 1: Single row result (QueryRow)

```go
row := db.pool.QueryRow(ctx, query, arg1, arg2)
err := row.Scan(&dest1, &dest2, ...)
// if err == pgx.ErrNoRows → store.ErrNotFound
```

Used by: `GetJob`, `GetJobByIdempotencyKey`, `TransitionJobState`

### Pattern 2: Multiple rows (Query + loop)

```go
rows, err := db.pool.Query(ctx, query, args...)
defer rows.Close()   // ALWAYS defer immediately after Query
for rows.Next() {
    if err := rows.Scan(...); err != nil { ... }
}
return results, rows.Err()  // ALWAYS check rows.Err() after loop
```

Used by: `ListJobs`, `ClaimPendingJobs`, `GetExecutions`, `ListActiveWorkers`

### Pattern 3: Mutation (Exec)

```go
result, err := db.pool.Exec(ctx, query, args...)
if result.RowsAffected() == 0 { return store.ErrNotFound }
```

Used by: `RegisterWorker`, `Heartbeat`, `DeregisterWorker`, `DeleteJob`

---

## The CAS pattern — `TransitionJobState`

This is the most important method. Read it carefully.

```go
func (db *DB) TransitionJobState(
    ctx context.Context,
    id uuid.UUID,
    expectedStatus, newStatus domain.JobStatus,
    opts ...TransitionOption,
) error
```

### What it does

Builds and executes this SQL:
```sql
UPDATE jobs
SET status = $3
    [, worker_id = $4]        -- if WithWorkerID() provided
    [, started_at = $5]       -- if WithStartedAt() provided
    [, error_message = $6]    -- if WithError() provided
    [, next_retry_at = $7]    -- if WithNextRetryAt() provided
    [, completed_at = $8]     -- if WithCompletedAt() provided
    [, attempt = attempt + 1] -- automatically added when newStatus = failed
WHERE id = $1 AND status = $2   ← THE CAS CHECK
RETURNING id
```

### Why this is atomic

The `AND status = $2` is the CAS guard. If another process has already changed the job's status, the WHERE clause matches 0 rows. PostgreSQL's RETURNING clause then returns 0 rows, which pgx surfaces as `pgx.ErrNoRows`. We convert that to `store.ErrStateConflict`.

```
Scheduler-A and Scheduler-B both try to claim job-123:

Scheduler-A: UPDATE WHERE id='123' AND status='queued' → 1 row updated ✓
Scheduler-B: UPDATE WHERE id='123' AND status='queued' → 0 rows (status is now 'scheduled') → ErrStateConflict
```

Scheduler-B's caller sees `ErrStateConflict` and skips to the next job. Job-123 is dispatched exactly once.

### Dynamic SET clause

The `TransitionOption` functional options pattern avoids having dozens of separate methods. Instead of `MarkJobRunningWithWorkerIDAndStartedAt(...)`, you call:

```go
store.TransitionJobState(ctx, id, JobStatusScheduled, JobStatusRunning,
    store.WithWorkerID("worker-prod-3"),
    store.WithStartedAt(time.Now()),
)
```

Each option is applied to a `TransitionUpdateExported` struct, then each non-nil field becomes an additional `$N` clause in the SET list.

---

## The idempotency pattern — `CreateJob`

```go
func (db *DB) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error)
```

Three code paths:

### Path A — Normal (no idempotency key)
```
INSERT INTO jobs (...) VALUES (...)
→ success → return new job
```

### Path B — Idempotent retry (key already exists)
```
SELECT WHERE idempotency_key = 'key' → found existing job
→ return existing job (no INSERT attempted)
```

### Path C — Concurrent race (two requests, same key, at the same moment)
```
Request-A: SELECT → not found → INSERT → succeeds
Request-B: SELECT → not found → INSERT → fails with 23505 unique_violation
Request-B: catches 23505 → SELECT WHERE idempotency_key = 'key' → finds Request-A's job → returns it
```

Both callers receive the same job. The UNIQUE constraint on `idempotency_key` is the safety net.

---

## Scanning — how SQL rows become Go structs

### The `jobColumns` constant

```go
const jobColumns = `
    id, idempotency_key, name, type, queue_name, priority,
    status, payload, max_retries, attempt,
    worker_id, error_message,
    scheduled_at, deadline, next_retry_at, started_at, completed_at,
    created_at, updated_at`
```

Every SELECT uses this constant to guarantee the column order matches `scanJob`. If you add a column, add it to both in the same position.

### Why intermediate variables?

```go
var (
    idempotencyKey *string   // nullable VARCHAR → *string (nil means NULL)
    typeStr        string    // enum stored as text → convert to domain.JobType
    statusStr      string    // enum stored as text → convert to domain.JobStatus
    payloadJSON    []byte    // JSONB → unmarshal after scan
    workerID       *string   // nullable VARCHAR
    errorMessage   *string   // nullable TEXT
    priority       int16     // SMALLINT → convert to domain.JobPriority
)
```

You cannot scan SQL NULL directly into a Go `string` — pgx returns an error. You must scan into `*string` and then dereference if non-nil:
```go
if workerID != nil {
    j.WorkerID = *workerID
}
```

The `*time.Time` fields (`ScheduledAt`, `StartedAt`, etc.) work differently — pgx handles NULL → nil automatically for pointer types.

---

## The `nullableString` helper

```go
func nullableString(s string) *string {
    if s == "" { return nil }
    return &s
}
```

Why? PostgreSQL's UNIQUE constraint treats multiple NULL values as distinct (NULL ≠ NULL in SQL). If we stored empty string `""` instead of NULL for missing idempotency keys, the UNIQUE constraint would allow only one job with no key. Storing NULL allows unlimited jobs with no key.

---

## The `isUniqueViolation` helper

```go
func isUniqueViolation(err error) bool {
    msg := err.Error()
    return strings.Contains(msg, "23505") ||
           strings.Contains(msg, "unique_violation") ||
           strings.Contains(msg, "duplicate key")
}
```

PostgreSQL error code 23505 = unique constraint violation. We check the error message string because pgx wraps errors and the underlying `*pgconn.PgError` type may be buried. Checking the string is simpler and reliable.

---

## `ReclaimOrphanedJobs` — crash recovery

```sql
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
SET status = 'queued', worker_id = NULL,
    error_message = 'reclaimed: worker went silent'
FROM orphaned
WHERE jobs.id = orphaned.id
RETURNING jobs.id
```

This query finds every job in `running` state whose worker either:
- Has no heartbeat in the last `staleThreshold` seconds (90s default)
- No longer exists in the workers table

And resets them to `queued` so the scheduler can re-dispatch them.

The `$1::interval` syntax casts a string like `"90s"` to a PostgreSQL interval type. We use this instead of `NOW() - $1 * INTERVAL '1 second'` because the latter requires extra casting arithmetic.

---

## File location

```
orion/
└── internal/
    └── store/
        ├── store.go              ← interface (do not add SQL here)
        └── postgres/
            └── db.go             ← you are here (all SQL lives here)
```

---

## Common mistakes

| Mistake | Symptom | Fix |
|---|---|---|
| Forgetting `defer rows.Close()` | Pool exhausted after ~20 requests | Always defer immediately after `Query()` |
| Not checking `rows.Err()` | Silent partial results | Always `return ..., rows.Err()` |
| Scanning NULL into `string` | pgx scan error | Use `*string` for nullable columns |
| Adding column to `jobColumns` but not `scanJob` | Scan error (wrong number of values) | Update both in the same commit |
| Not handling `pgx.ErrNoRows` | 500 instead of 404 | Check `errors.Is(err, pgx.ErrNoRows)` in every QueryRow |