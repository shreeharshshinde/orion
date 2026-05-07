# Fair Queue — Phase 8

## What This Document Covers

The weighted fair scheduler added in Phase 8: the starvation problem it solves, the `ComputeAllocations` algorithm, the `FairQueue` implementation in `internal/scheduler/fairqueue.go`, and how it interacts with the existing priority system.

---

## The Starvation Problem

Before Phase 8, `scheduleQueuedJobs` ran a single query:

```sql
SELECT * FROM jobs WHERE status = 'queued' ORDER BY priority DESC, created_at ASC LIMIT 50
```

This is correct for priority ordering within a single queue but breaks across queues:

```
Scenario: 100 low-priority jobs + 5 high-priority jobs, batchSize=50

Query result (ORDER BY priority DESC):
  jobs 1–5:   high-priority (dispatched ✓)
  jobs 6–50:  low-priority  (dispatched ✓)
  jobs 51–100: low-priority (waiting)

Next tick: 45 more low-priority jobs still waiting
  → 45 low-priority dispatched
  → high queue gets 0 service this tick

Result: high queue is fine. But if the situation were reversed —
  100 low-priority jobs submitted first, then 5 high-priority:
  → All 50 slots go to low-priority (they were queued first)
  → High-priority jobs wait until low-priority backlog clears
  → SLA broken
```

The root cause: a single `ORDER BY priority` query treats all queues as one pool. A queue with many jobs always wins over a queue with few jobs, regardless of weight.

---

## The Solution: Per-Queue Allocation

Phase 8 replaces the single query with per-queue queries, each with its own `LIMIT`:

```
ComputeAllocations(batchSize=50, weights: high=0.8, default=0.6, low=0.2)
→ high:    LIMIT 40
→ default: LIMIT 30 (capped to remaining 10)
→ low:     LIMIT 10 (capped to remaining 0)

FetchReadyJobs():
  SELECT * FROM jobs WHERE status='queued' AND queue_name='orion:queue:high' LIMIT 40
  SELECT * FROM jobs WHERE status='queued' AND queue_name='orion:queue:default' LIMIT 30
  SELECT * FROM jobs WHERE status='queued' AND queue_name='orion:queue:low' LIMIT 10
```

Each queue is isolated. A flood of low-priority jobs cannot consume the high queue's allocation.

---

## `ComputeAllocations` Algorithm

```go
func ComputeAllocations(queueCfgs map[string]QueueAllocation, batchSize int) []QueueAllocation
```

```
Input:  batchSize=50, queues sorted by weight descending
        [high=0.8, default=0.6, low=0.2]

remaining = 50

high:
  limit = floor(50 × 0.8) = 40
  limit = min(40, remaining=50) = 40
  remaining = 50 - 40 = 10

default:
  limit = floor(50 × 0.6) = 30
  limit = min(30, remaining=10) = 10
  remaining = 10 - 10 = 0

low:
  remaining = 0 → limit = 0

Result: [high=40, default=10, low=0]
```

### Unused capacity flows down

If high only has 3 jobs ready (not 40):

```
high:    3 jobs fetched (used 3 of 40 allocation)
         remaining = 50 - 3 = 47

default: min(30, 47) = 30
         remaining = 47 - 30 = 17

low:     min(10, 17) = 10
         remaining = 7 (unused this tick)
```

Low-priority jobs get service whenever high-priority queues have spare capacity. They are never completely starved.

### Why sort by weight descending

High-weight queues get first claim on `remaining`. If we processed low-weight queues first, they could consume capacity that should go to high-weight queues. Descending order ensures the priority hierarchy is respected.

### Weights are proportions, not fractions

Weights don't need to sum to 1.0. `high=0.8, default=0.6, low=0.2` means high gets 80% of `batchSize`, default gets 60%, low gets 20% — but since they're processed sequentially with a shared `remaining` counter, the actual allocations depend on what's left after each step.

---

## `FairQueue` Implementation

```go
type FairQueue struct {
    queues  []QueueAllocation  // sorted: highest weight first
    limiter *QueueRateLimiter
    store   store.Store
    logger  *slog.Logger
}
```

### `FetchReadyJobs(ctx) ([]*domain.Job, error)`

Queries each queue independently. A single queue's DB error is logged and skipped — it does not block other queues. Returns all jobs in weight-descending order (high-priority jobs first in the returned slice).

### `AllowDispatch(queueName string) bool`

Delegates to `rateLimiter.Allow(queueName)`. Called by the scheduler for each job after `FetchReadyJobs` returns. Jobs that fail this check are skipped this tick.

---

## Integration with the Scheduler

`scheduleQueuedJobs` in `internal/scheduler/scheduler.go`:

```go
// Phase 8 path: fair queue with rate limiting
if len(s.queueAllocations) > 0 {
    allocations := ComputeAllocations(s.queueAllocations, s.cfg.Scheduler.BatchSize)
    fq := NewFairQueue(allocations, s.rateLimiter, s.store, s.logger)
    jobs, err = fq.FetchReadyJobs(ctx)
} else {
    // Phase 7 fallback: single query (used in tests)
    jobs, err = s.store.ListJobs(ctx, store.JobFilter{Status: &queued, Limit: batchSize})
}

dispatched, rateLimited := 0, 0
for _, job := range jobs {
    if !fq.AllowDispatch(job.QueueName) {
        rateLimited++
        metrics.QueueRateLimited.WithLabelValues(job.QueueName).Inc()
        continue
    }
    // CAS: UPDATE status=scheduled WHERE status=queued
    // XADD to Redis stream
    dispatched++
}

logger.Info("scheduler cycle", "dispatched", dispatched, "rate_limited", rateLimited)
```

The fallback to `ListJobs` when `queueAllocations` is empty means all existing unit tests pass without modification — they don't wire Phase 8 components.

---

## Interaction with the Priority System

Phase 8 does not replace the existing `priority` field on jobs. The two systems operate at different layers:

| Layer | Mechanism | Controls |
|---|---|---|
| **Cross-queue** | Weighted fair queue | Which queue gets how many dispatch slots per tick |
| **Within-queue** | `ORDER BY priority DESC` | Which jobs within a queue dispatch first |

A high-priority job in the `low` queue still dispatches before a low-priority job in the same queue. But the `low` queue as a whole gets fewer slots than the `high` queue.

Example:
```
high queue: 5 jobs, all priority=1 (low)
low queue:  5 jobs, all priority=9 (high)

With fair scheduling (batchSize=10, high=0.8, low=0.2):
  high queue gets 8 slots → dispatches all 5 priority=1 jobs
  low queue gets 2 slots  → dispatches 2 of 5 priority=9 jobs

The queue name determines capacity allocation.
The priority field determines ordering within that capacity.
```

This is intentional. Queue names represent workload classes (interactive vs. batch). Priority represents urgency within a class. They are orthogonal dimensions.

---

## Observability

### Scheduler logs

Every dispatch cycle logs:
```
INFO msg="scheduler cycle" dispatched=45 rate_limited=5 duration_ms=12
```

`rate_limited > 0` means the rate limiter is actively throttling. `dispatched < batchSize` with `rate_limited = 0` means queues have fewer jobs than their allocations — system is not under load.

### Prometheus metrics

`orion_queue_dispatch_weight{queue="orion:queue:high"}` — published at startup and on config reload. Visible in Grafana to confirm weights are applied correctly.

`orion_queue_concurrency_limit{queue="orion:queue:high"}` — the `max_concurrent` value. Compare against `orion_worker_active_jobs` to see how close each queue is to its concurrency ceiling.
