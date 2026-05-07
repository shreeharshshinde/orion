# Phase 8 Implementation Record
## Rate Limiting, Fair Scheduling, and Queue Capacity Management

> **What this document is:** A complete record of what was built in Phase 8, why each decision was made, and how to verify the implementation. Written after the fact as a reference for future phases and code reviewers.

---

## What Phase 8 Added

Before Phase 8, all three queues competed for worker slots on a first-come-first-served basis. A flood of low-priority jobs could starve high-priority work indefinitely. After Phase 8:

- Each queue has a **token bucket rate limiter** — controls how fast jobs can be dispatched (jobs/second)
- The scheduler uses a **weighted fair queue** — each queue gets a proportional slice of each dispatch batch
- Queue configuration is **stored in PostgreSQL** and reloadable at runtime via `PUT /queues/{name}` — no restart needed
- Four new **Prometheus metrics** expose queue health in real time

---

## Files Created

| File | Purpose |
|---|---|
| `internal/scheduler/ratelimiter.go` | Thread-safe token bucket per queue |
| `internal/scheduler/fairqueue.go` | Weighted fair dispatch across queues |
| `internal/api/handler/queue.go` | HTTP handlers for `/queues` CRUD + stats |
| `internal/store/migrations/003_queue_config.up.sql` | Creates `queue_config` table, seeds defaults |
| `internal/store/migrations/003_queue_config.down.sql` | Drops `queue_config` table |

## Files Modified

| File | Change |
|---|---|
| `internal/store/store.go` | Added `QueueConfig` struct + `QueueConfigStore` interface embedded in `Store` |
| `internal/store/postgres/db.go` | Implemented `ListQueueConfigs`, `GetQueueConfig`, `UpsertQueueConfig` |
| `internal/config/config.go` | Added `QueueLimitConfig` + `QueueConfig` types; loaded from `ORION_QUEUE_*` env vars |
| `internal/observability/observability.go` | Added 4 new Prometheus metrics |
| `internal/scheduler/scheduler.go` | Added `rateLimiter` + `queueAllocations` fields; wired into dispatch loop |
| `cmd/scheduler/main.go` | Builds `QueueRateLimiter` + `queueAllocations` from config; passes to `scheduler.New` |
| `cmd/api/main.go` | Adds Redis client, `QueueRateLimiter`, registers 4 `/queues` routes |
| `internal/api/grpc/server_test.go` | Added 3 `QueueConfigStore` stubs to `fakeStore` |
| `internal/api/handler/job_test.go` | Same stubs |
| `internal/api/handler/pipeline_test.go` | Same stubs |
| `internal/pipeline/advancement_test.go` | Same stubs |

---

## Key Decisions

### 1. Token bucket over leaky bucket

The token bucket allows bursting — a queue that has been idle accumulates tokens up to `Burst` capacity and can dispatch that many jobs instantly when work arrives. This is correct for ML workloads where jobs arrive in batches (a pipeline submits 50 nodes at once).

A leaky bucket enforces a strict constant rate with no burst allowance, which would artificially delay the first batch of jobs even when the system is completely idle.

### 2. `float64` tokens, not `int`

At 10 tokens/second with a 2-second scheduler tick, integer arithmetic adds 20 tokens per tick — coarse and correct. But at 0.5 tokens/second (very slow queue), integer arithmetic adds 0 tokens per tick and the rate limiter never fires. `float64` allows fractional accumulation: 0.5 tokens/sec × 2s = 1.0 token added per tick.

### 3. Lazy refill on `Allow()`, not a background goroutine

Tokens are refilled inside `Allow()` based on elapsed time since `lastRefill`. No background goroutine is needed. This is simpler, has no goroutine leak risk, and is accurate — refill is proportional to actual elapsed time, not a fixed tick interval.

### 4. Weighted fair queue at the scheduler, not the worker

The fair scheduling decision happens in `scheduleQueuedJobs` (scheduler), not in the worker pool's dequeue loop. This is the right layer because:
- The scheduler controls which jobs enter Redis streams — it's the gatekeeper
- The worker pool is a pure consumer; it should not need to know about queue weights
- Changing weights requires no worker restart — only the scheduler's next tick

### 5. `ComputeAllocations` processes queues in weight-descending order

High-weight queues get first claim on the batch. Unused capacity (when a high-weight queue has fewer jobs than its allocation) flows to lower-weight queues. This means low-priority queues are never completely starved — they get whatever capacity high-priority queues don't use.

### 6. `queue_config` table with `ON CONFLICT DO NOTHING` seed

The migration seeds the three standard queues with production defaults. `ON CONFLICT DO NOTHING` means re-running the migration (e.g., in a fresh staging environment) doesn't overwrite operator-configured values. The table is the source of truth; env vars are only the startup default.

### 7. `PUT /queues/{name}` applies immediately to the in-process rate limiter

After writing to PostgreSQL, `UpdateQueue` calls `rateLimiter.UpdateConfig(...)` directly. This means the rate limiter in the running API process updates instantly. The scheduler's rate limiter updates on its next DB reload tick (≤2 seconds). No restart needed for either.

### 8. Backward-compatible scheduler fallback

`scheduleQueuedJobs` checks whether `queueAllocations` is configured before using `FairQueue`. If not configured (e.g., in unit tests that don't wire Phase 8 components), it falls back to the Phase 7 `ListJobs` path. All existing tests pass without modification.

---

## The Token Bucket Algorithm

```
State per queue:
  tokens     float64   — current token count
  maxTokens  float64   — burst capacity
  refillRate float64   — tokens/second
  lastRefill time.Time — when tokens were last added

On Allow(queueName):
  elapsed = now - lastRefill
  tokens  = min(maxTokens, tokens + elapsed × refillRate)
  lastRefill = now

  if tokens < 1.0:
    return false  // rate limited — skip this job this tick

  tokens -= 1.0
  return true     // dispatch allowed
```

All buckets start full (`tokens = burst`) so the first batch dispatches immediately without waiting for tokens to accumulate.

---

## The Fair Scheduling Algorithm

```
Input:  batchSize=50, weights: high=0.8, default=0.6, low=0.2
Output: per-queue dispatch limits

Step 1 — Sort by weight descending:
  [high=0.8, default=0.6, low=0.2]

Step 2 — Allocate proportionally, highest first:
  high:    floor(50 × 0.8) = 40, remaining = 50-40 = 10
  default: floor(50 × 0.6) = 30, but only 10 remaining → limit=10, remaining=0
  low:     remaining=0 → limit=0

Result: high=40, default=10, low=0

Step 3 — Unused capacity flows down:
  If high only has 3 jobs ready:
    high=3 (used 3 of 40), remaining = 50-3 = 47
    default: min(30, 47) = 30, remaining = 17
    low: min(10, 17) = 10, remaining = 7
    (7 slots unused this tick)
```

Each queue is queried independently with its own `LIMIT`. A single queue's backlog cannot consume the entire batch.

---

## New API Endpoints

Four routes registered in `cmd/api/main.go`:

```
GET  /queues                   → list all queue configurations
GET  /queues/{name}            → get config for one queue
PUT  /queues/{name}            → update config (live reload, no restart)
GET  /queues/{name}/stats      → real-time depth + token availability
```

### `PUT /queues/{name}` — partial update

All fields are optional. Only provided fields are updated:

```bash
# Halve the low queue's rate limit
curl -X PUT http://localhost:8080/queues/orion:queue:low \
  -H "Content-Type: application/json" \
  -d '{"rate_per_sec": 5.0, "burst": 3}'

# Disable a queue entirely (pauses all dispatch)
curl -X PUT http://localhost:8080/queues/orion:queue:low \
  -d '{"enabled": false}'
```

### `GET /queues/{name}/stats` — real-time health

```bash
curl http://localhost:8080/queues/orion:queue:high/stats
# {
#   "queue_name": "orion:queue:high",
#   "depth": 42,              ← pending messages in Redis stream
#   "rate_tokens_avail": 18.3 ← current token bucket level
# }
```

`depth` comes from `queue.Len()` (Redis `XLEN`). `rate_tokens_avail` comes from `rateLimiter.Available()`. Both are nil-safe — if Redis is unreachable, `depth` returns 0.

---

## New Prometheus Metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `orion_queue_rate_limited_total` | Counter | `queue` | Jobs skipped due to rate limiting per scheduler cycle |
| `orion_queue_concurrent_jobs` | Gauge | `queue` | Current number of running jobs from this queue |
| `orion_queue_concurrency_limit` | Gauge | `queue` | Configured `max_concurrent` for this queue |
| `orion_queue_dispatch_weight` | Gauge | `queue` | Configured `weight` for this queue |

`orion_queue_rate_limited_total` is the key operational metric — a sustained non-zero value means the queue is consistently hitting its rate limit and may need tuning.

---

## Database Schema

Migration `003_queue_config.up.sql` creates:

```sql
CREATE TABLE queue_config (
    queue_name      TEXT        PRIMARY KEY,
    max_concurrent  INT         NOT NULL DEFAULT 10,
    weight          FLOAT       NOT NULL DEFAULT 0.5,
    rate_per_sec    FLOAT       NOT NULL DEFAULT 50.0,
    burst           INT         NOT NULL DEFAULT 10,
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Seeded with production defaults:

| queue_name | max_concurrent | weight | rate_per_sec | burst |
|---|---|---|---|---|
| `orion:queue:high` | 8 | 0.8 | 100.0 | 20 |
| `orion:queue:default` | 6 | 0.6 | 50.0 | 10 |
| `orion:queue:low` | 2 | 0.2 | 10.0 | 5 |

A partial index on `enabled = TRUE` speeds up the scheduler's reload query — disabled queues are excluded from the scan.

---

## Configuration

Queue limits are loaded from `ORION_QUEUE_*` env vars at startup. Defaults are derived from `ORION_WORKER_CONCURRENCY` (default 10):

| Env var | Default | Description |
|---|---|---|
| `ORION_QUEUE_HIGH_MAX_CONCURRENT` | 80% of concurrency | Max worker slots for high queue |
| `ORION_QUEUE_HIGH_WEIGHT` | `0.8` | Dispatch weight |
| `ORION_QUEUE_HIGH_RATE_PER_SEC` | `100.0` | Token refill rate |
| `ORION_QUEUE_HIGH_BURST` | `20` | Burst capacity |
| `ORION_QUEUE_DEFAULT_MAX_CONCURRENT` | 60% of concurrency | — |
| `ORION_QUEUE_DEFAULT_WEIGHT` | `0.6` | — |
| `ORION_QUEUE_DEFAULT_RATE_PER_SEC` | `50.0` | — |
| `ORION_QUEUE_DEFAULT_BURST` | `10` | — |
| `ORION_QUEUE_LOW_MAX_CONCURRENT` | 20% of concurrency | — |
| `ORION_QUEUE_LOW_WEIGHT` | `0.2` | — |
| `ORION_QUEUE_LOW_RATE_PER_SEC` | `10.0` | — |
| `ORION_QUEUE_LOW_BURST` | `5` | — |

Env vars set the startup state. The `queue_config` table is the runtime source of truth — the scheduler reloads it on every tick.

---

## Test Stubs

`QueueConfigStore` was added to the `Store` interface. All four existing fake stores needed three new stub methods to satisfy the interface:

```go
func (f *fakeStore) ListQueueConfigs(_ context.Context) ([]*store.QueueConfig, error) {
    return nil, nil
}
func (f *fakeStore) GetQueueConfig(_ context.Context, _ string) (*store.QueueConfig, error) {
    return nil, store.ErrNotFound
}
func (f *fakeStore) UpsertQueueConfig(_ context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) {
    return cfg, nil
}
```

Added to: `server_test.go`, `job_test.go`, `pipeline_test.go`, `advancement_test.go`. No new test logic in these files — Phase 8 queue tests live in the new files.

---

## Verification

### Apply migration

```bash
make migrate-up
# Applies 003_queue_config.up.sql
# Creates queue_config table with 3 seeded rows
```

### Verify seeded rows

```bash
psql $DB_DSN -c "SELECT queue_name, weight, rate_per_sec, burst FROM queue_config;"
#      queue_name       | weight | rate_per_sec | burst
# ----------------------+--------+--------------+-------
#  orion:queue:default  |    0.6 |           50 |    10
#  orion:queue:high     |    0.8 |          100 |    20
#  orion:queue:low      |    0.2 |           10 |     5
```

### List queues via API

```bash
make run-api  # in one terminal
curl -s http://localhost:8080/queues | jq .
# { "queues": [...], "count": 3 }
```

### Live reload test

```bash
# Reduce low queue rate to 1 job/second
curl -X PUT http://localhost:8080/queues/orion:queue:low \
  -H "Content-Type: application/json" \
  -d '{"rate_per_sec": 1.0, "burst": 1}'

# Submit 10 low-priority jobs
for i in $(seq 1 10); do
  curl -s -X POST http://localhost:8080/jobs \
    -d '{"name":"rate-test","type":"inline","queue_name":"low","payload":{"handler_name":"noop"}}' \
    > /dev/null
done

# Watch scheduler logs — should see "rate_limited=9" on first tick
# (only 1 token available, 9 jobs skipped)
make run-scheduler
```

### Verify fair scheduling

```bash
# Submit 100 low-priority and 10 high-priority jobs simultaneously
for i in $(seq 1 100); do
  curl -s -X POST http://localhost:8080/jobs \
    -d '{"name":"low-flood","type":"inline","queue_name":"low","payload":{"handler_name":"noop"}}' \
    > /dev/null &
done
for i in $(seq 1 10); do
  curl -s -X POST http://localhost:8080/jobs \
    -d '{"name":"high-urgent","type":"inline","queue_name":"high","payload":{"handler_name":"noop"}}' \
    > /dev/null
done

# High-priority jobs should complete first despite being submitted after the flood
```

---

## Architecture After Phase 8

```
Scheduler dispatch loop (every 2s):
  1. ComputeAllocations(batchSize=50, weights)
     → high=40, default=30, low=10

  2. FairQueue.FetchReadyJobs()
     → SELECT ... WHERE queue_name='orion:queue:high' LIMIT 40
     → SELECT ... WHERE queue_name='orion:queue:default' LIMIT 30
     → SELECT ... WHERE queue_name='orion:queue:low' LIMIT 10

  3. For each job:
     if rateLimiter.Allow(job.QueueName):
       UPDATE status=scheduled
       XADD orion:queue:{name}
     else:
       skip (increment orion_queue_rate_limited_total)

  4. Log: dispatched=N rate_limited=M
```

---

## What Phase 9 Changes

Phase 9 (Helm + Kubernetes) adds `ORION_QUEUE_*` env vars to the Helm `ConfigMap` template, sourced from `values.yaml`. The `queue_config` table remains the runtime source of truth — operators use `PUT /queues/{name}` to tune limits without touching Helm values or redeploying.
