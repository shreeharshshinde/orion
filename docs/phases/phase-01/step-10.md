# Step 10 — Scheduler (`internal/scheduler/scheduler.go`)

## What is this file?

The Scheduler is the **brain of job dispatch**. It runs as its own process (`cmd/scheduler/main.go`) and does three things in loops:

1. **Dispatch**: picks up `queued` jobs from PostgreSQL and pushes them to Redis
2. **Retry promotion**: finds `failed` jobs whose retry delay has expired and requeues them
3. **Orphan reclaim**: finds `running` jobs whose worker has gone silent and requeues them

## Why a separate scheduler process?

You might wonder: why not just let workers pull directly from PostgreSQL?

```
Workers polling PostgreSQL directly:
Worker-1: SELECT * FROM jobs WHERE status='queued' LIMIT 1 FOR UPDATE
Worker-2: SELECT * FROM jobs WHERE status='queued' LIMIT 1 FOR UPDATE
Worker-3: SELECT * FROM jobs WHERE status='queued' LIMIT 1 FOR UPDATE
... 50 workers × every second = 50 queries/second of database load just for polling
```

With a central scheduler:
```
Scheduler: one SELECT every 2s → pushes to Redis
Workers: XREADGROUP (blocking, no DB load)
```

The scheduler acts as a translator between the durable store (PostgreSQL) and the fast queue (Redis). Database load drops dramatically.

## Leader Election — only one scheduler runs

What if you run 3 scheduler pods in Kubernetes for redundancy? You need exactly ONE to be active at a time. If two schedulers dispatch the same job, the job runs twice.

**Solution: PostgreSQL advisory locks**
```go
const advisoryLockKey = 7331001  // arbitrary stable integer

func (s *Scheduler) tryAcquireLeaderLock(ctx context.Context) (bool, error) {
    var held bool
    err := s.db.QueryRow(ctx,
        "SELECT pg_try_advisory_lock($1)", advisoryLockKey,
    ).Scan(&held)
    return held, err
}
```

`pg_try_advisory_lock` is a PostgreSQL built-in that returns `true` if your connection acquired the lock, `false` if another connection already holds it.

**Key property**: the lock is automatically released when the connection closes — even if the process crashes. No manual cleanup needed.

```
Scheduler Pod-1: pg_try_advisory_lock → true  (LEADER)
Scheduler Pod-2: pg_try_advisory_lock → false (standby, retries every 3s)
Scheduler Pod-3: pg_try_advisory_lock → false (standby)

Pod-1 crashes:
  → connection closes
  → advisory lock released automatically
  → Pod-2 acquires lock within 3 seconds → becomes LEADER
```

Full rationale in `docs/adr/ADR-002-leader-election.md`.

## The Run() loop

```go
func (s *Scheduler) Run(ctx context.Context) error {
    for {
        held, err := s.tryAcquireLeaderLock(ctx)

        if !held {
            // Another instance is leader. Wait 3s, try again.
            time.Sleep(3 * time.Second)
            continue
        }

        // We are the leader. Run until we lose the lock or ctx is cancelled.
        s.runAsLeader(ctx)
    }
}
```

## runAsLeader() — the actual work

```go
func (s *Scheduler) runAsLeader(ctx context.Context) {
    scheduleTicker := time.NewTicker(2 * time.Second)  // dispatch + retry
    orphanTicker := time.NewTicker(30 * time.Second)   // orphan reclaim

    for {
        select {
        case <-scheduleTicker.C:
            s.scheduleQueuedJobs(ctx)    // dispatch loop
            s.promoteRetryableJobs(ctx)  // retry loop
        case <-orphanTicker.C:
            s.reclaimOrphanedJobs(ctx)   // orphan sweep
        case <-ctx.Done():
            return  // SIGTERM received, clean shutdown
        }
    }
}
```

## scheduleQueuedJobs() — the dispatch loop

Every 2 seconds:
```sql
-- Step 1: Find queued jobs (uses the partial index — fast)
SELECT * FROM jobs WHERE status = 'queued'
ORDER BY priority DESC, created_at ASC
LIMIT 50;
```

For each job:
```sql
-- Step 2: CAS transition — only succeeds if job is still 'queued'
UPDATE jobs SET status = 'scheduled'
WHERE id = $1 AND status = 'queued'  -- CAS check
RETURNING id;
-- Returns 0 rows if another scheduler already claimed it
```

```redis
-- Step 3: Push to Redis (only if CAS succeeded)
XADD orion:queue:default * job_id "abc" payload "{...}"
```

If the Redis push fails (Redis is down), we roll back the PostgreSQL transition:
```sql
-- Rollback: scheduled → queued
UPDATE jobs SET status = 'queued' WHERE id = $1 AND status = 'scheduled'
```

This keeps PostgreSQL and Redis consistent. A job that's in Redis is always in `scheduled` state in PostgreSQL.

## promoteRetryableJobs() — the retry loop

Every 2 seconds:
```sql
SELECT * FROM jobs WHERE status = 'failed'
AND attempt < max_retries
AND next_retry_at <= NOW()
LIMIT 50;
```

For each eligible job, two-step CAS:
```
failed → retrying → queued
```

Why two steps instead of one? The intermediate `retrying` state gives visibility into which jobs are currently being promoted. If the process crashes between the two transitions, the scheduler will pick them up again next cycle.

## reclaimOrphanedJobs() — crash recovery

Every 30 seconds:
```sql
-- Find jobs that are 'running' but their worker hasn't heartbeated recently
SELECT j.id FROM jobs j
WHERE j.status = 'running'
AND j.worker_id NOT IN (
    SELECT id FROM workers
    WHERE last_heartbeat > NOW() - INTERVAL '90 seconds'
);
-- Transition these jobs back to 'queued'
```

The 90-second threshold is `workerHeartbeatTTL * 2`:
- Workers heartbeat every 15 seconds
- A worker that's slow/overloaded might miss 1-2 heartbeats
- If a worker misses 6+ heartbeats (90s), it's definitely dead

## File location
```
orion/
└── internal/
    └── scheduler/
        └── scheduler.go   ← you are here
```

## Next step
The scheduler pushes jobs to Redis. Now we build the **Worker Pool** — the goroutine system that pulls jobs from Redis and actually executes them.