# ADR-003: CAS State Transitions via `UPDATE WHERE status = expected`

**Date:** 2024-01-15
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

Orion's job state machine has 8 states and strict valid transition rules. Multiple processes touch job state simultaneously:

- The **scheduler** transitions `queued → scheduled`
- The **worker** transitions `scheduled → running`, then `running → completed/failed`
- The **scheduler's orphan sweep** transitions `running → queued` when a worker dies

Without coordination, two schedulers running simultaneously could both claim the same job:

```
Scheduler A: SELECT WHERE status='queued' → finds job X
Scheduler B: SELECT WHERE status='queued' → finds job X (same row)
Scheduler A: UPDATE SET status='scheduled' WHERE id=X  → succeeds
Scheduler B: UPDATE SET status='scheduled' WHERE id=X  → also succeeds (race!)
→ Job X dispatched to Redis twice → two workers execute it → duplicate execution
```

## Decision

Use **Compare-And-Swap (CAS) via a single atomic SQL statement** for every state transition:

```sql
UPDATE jobs
SET    status = $new_status [, worker_id = $worker_id, started_at = NOW(), ...]
WHERE  id = $1
AND    status = $expected_status   -- the atomic guard
RETURNING id
```

If `RETURNING id` returns zero rows, the job's status was not what we expected — another process changed it first. Return `ErrStateConflict` to the caller; it is safe to skip this job.

Evaluated options:

| Option | Atomicity | Complexity | Extra infrastructure |
|---|---|---|---|
| SELECT then UPDATE (two queries) | ❌ race window | Low | None |
| SELECT FOR UPDATE (advisory row lock) | ✅ | Medium | None |
| **CAS: UPDATE WHERE status = expected** | ✅ | Low | None |
| Distributed lock (Redis SETNX) | ✅ | High | Redis lock infra |
| Optimistic concurrency with version field | ✅ | Medium | Extra column |

CAS in one SQL statement is the simplest correct solution. PostgreSQL guarantees the `WHERE status = expected` check and the `SET status = new` happen atomically within a single statement — no transaction needed for this specific pattern.

## Consequences

**Positive:**
- Zero extra infrastructure — CAS is pure SQL
- Concurrent schedulers are safe: at most one can claim any given job
- `ErrStateConflict` is a first-class sentinel — callers handle it explicitly
- The pattern is composable: any process calling `MarkJobRunning` gets the same safety guarantee
- Second-layer defense: even if leader election fails, double-execution is still prevented

**Negative:**
- Every state transition must be expressed as a single `UPDATE WHERE` — cannot use multi-step business logic inside the transition
- High-frequency transitions under contention produce `ErrStateConflict` noise — callers must treat it as a non-error (log at Debug, not Error)

## Implementation Notes

All 8 state transitions are implemented as CAS calls in `internal/store/postgres/db.go`:

```go
// Example: MarkJobRunning
const q = `
    UPDATE jobs
    SET    status = 'running', worker_id = $2, started_at = NOW()
    WHERE  id = $1 AND status = 'scheduled'
    RETURNING id`

result, err := db.pool.Exec(ctx, q, id, workerID)
if result.RowsAffected() == 0 {
    return store.ErrStateConflict
}
```

`ErrStateConflict` uses `errors.Is()` matching so callers can check it without depending on string comparison.

## Migration Path

If write throughput exceeds PostgreSQL capacity (millions of jobs/second), migrate to optimistic concurrency with a `version INT` column. The `store.Store` interface is the abstraction boundary — callers do not change.