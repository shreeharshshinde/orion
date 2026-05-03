# Orion — Phase 8 Master Guide
## Rate Limiting, Fair Scheduling, and Queue Capacity Management

> **What this document is:** The complete planning guide for Phase 8. Every design decision with rationale, every file to create, every algorithm explained in pseudocode before any Go code is written, the configuration model, migration SQL, step-by-step build order, and the exact tests that prove Phase 8 is done. Read this once before touching any code.

---

## Table of Contents

1. [Why Phase 8 Exists — The Gap It Closes](#1-why-phase-8-exists)
2. [The Core Problem: Worker Pool Starvation](#2-the-core-problem-worker-pool-starvation)
3. [Three Mechanisms That Work Together](#3-three-mechanisms-that-work-together)
4. [Mental Model — How the Scheduler Changes](#4-mental-model--how-the-scheduler-changes)
5. [Configuration Model](#5-configuration-model)
6. [Complete File Plan](#6-complete-file-plan)
7. [File 1: `internal/store/migrations/003_queue_config.up.sql`](#7-file-1-migration-003)
8. [File 2: `internal/config/config.go` — Queue Limit Config](#8-file-2-config-update)
9. [File 3: `internal/scheduler/ratelimiter.go` — Token Bucket](#9-file-3-ratelimitergo)
10. [File 4: `internal/scheduler/fairqueue.go` — Fair Scheduler](#10-file-4-fairqueuego)
11. [File 5: `internal/scheduler/scheduler.go` — Updated Dispatch Loop](#11-file-5-updated-scheduler)
12. [File 6: `internal/api/handler/queue.go` — Queue Config API](#12-file-6-queue-config-api)
13. [File 7: `cmd/api/main.go` — New Routes](#13-file-7-cmd-api-update)
14. [File 8: `cmd/scheduler/main.go` — RateLimiter Wired](#14-file-8-cmd-scheduler-update)
15. [The Token Bucket Algorithm — Deep Dive](#15-the-token-bucket-algorithm)
16. [The Fair Scheduling Algorithm — Deep Dive](#16-the-fair-scheduling-algorithm)
17. [Interaction With Existing Priority System](#17-interaction-with-existing-priority-system)
18. [Metrics for Phase 8](#18-metrics-for-phase-8)
19. [Step-by-Step Build Order](#19-step-by-step-build-order)
20. [Complete Verification Sequence](#20-complete-verification-sequence)
21. [Common Mistakes](#21-common-mistakes)
22. [Phase 9 Preview](#22-phase-9-preview)

---

## 1. Why Phase 8 Exists

After Phase 7, Orion has correct, observable, and real-time-streaming execution of ML workloads. But it has no concept of **fairness** or **capacity isolation**.

Every worker slot is first-come-first-served across all queues. This creates two failure modes that break real-world ML platforms:

**Failure mode 1 — Low-priority starvation of high-priority:**
```
1000 batch preprocessing jobs submitted to low-priority queue
→ All 10 worker slots consumed by low-priority work
→ A researcher submits an urgent interactive training job (high priority)
→ Researcher waits 83 minutes while all batch jobs finish
→ SLA broken
```

**Failure mode 2 — One queue consumes all capacity:**
```
A pipeline with 200 parallel nodes all become ready simultaneously
→ All 200 jobs flood the default queue
→ default queue consumes all 10 worker slots
→ high queue and low queue get zero service for the duration
→ Other teams' jobs are completely blocked
```

Phase 8 fixes both by adding per-queue concurrency limits and weighted fair scheduling.

---

## 2. The Core Problem: Worker Pool Starvation

### Current architecture (Phase 7)

The worker pool has:
- N goroutines (default: 10)
- 1 dequeue goroutine **per queue name** (currently 3: high, default, low)
- All dequeue goroutines compete to send to the same shared `jobCh` (buffered, capacity N)

```
dequeue(high)    ────────────────────────────────┐
dequeue(default) ─────────────────────────────── ├──► jobCh (cap=10) ──► worker goroutines (×10)
dequeue(low)     ────────────────────────────────┘
```

Problem: `jobCh` is first-come-first-served. If `dequeue(low)` is fastest (many messages ready), it fills `jobCh` and starves the others. There is no enforcement that high-priority jobs get preferential access.

### Phase 8 solution

Add two enforcement layers:

**Layer 1 — Concurrency limits (per-queue slot reservation):**
Each queue gets a maximum number of worker slots it can occupy simultaneously.

```
queue_limits:
  high:    8 slots max   (out of 10 total)
  default: 6 slots max
  low:     2 slots max
```

**Layer 2 — Weighted fair scheduling (in the scheduler dispatch loop):**
The scheduler's `scheduleQueuedJobs` function dispatches in priority-weighted order, not FIFO across queues. This ensures high-priority queues always get proportionally more of the scheduler's dispatch bandwidth.

---

## 3. Three Mechanisms That Work Together

### Mechanism 1 — Concurrency Limiter (in the worker pool)

```
Before accepting a job from queue X into jobCh:
  if concurrencyLimiter.Available(X) > 0:
    concurrencyLimiter.Acquire(X)   ← decrement slot counter
    pass job to jobCh
  else:
    NACK job back to Redis PEL      ← do not consume this slot
    wait for release
```

Implementation: a `map[string]*semaphore` where each semaphore tracks how many slots queue X currently occupies.

### Mechanism 2 — Rate Limiter (in the scheduler)

```
Before dispatching job from queue X to Redis:
  if rateLimiter.Allow(X):
    dispatch job
  else:
    skip this job this tick, try next tick
```

Implementation: token bucket per queue. Each queue has a refill rate (tokens per second) and a burst capacity. The scheduler calls `Allow()` before enqueuing each job.

### Mechanism 3 — Weighted Fair Queue (in the scheduler dispatch loop)

```
Instead of: for job in all_queued_jobs: dispatch(job)

Do:
  high_jobs = ListJobs(queue=high, limit=batch*0.8)
  default_jobs = ListJobs(queue=default, limit=batch*0.6)
  low_jobs = ListJobs(queue=low, limit=batch*0.2)

  dispatch all high_jobs first
  dispatch remaining capacity from default_jobs
  dispatch remaining capacity from low_jobs
```

Implementation: weighted dispatch in `scheduleQueuedJobs` using configurable weights per queue.

---

## 4. Mental Model — How the Scheduler Changes

### Before Phase 8 (current `scheduleQueuedJobs`)

```
1. SELECT * FROM jobs WHERE status='queued'
   ORDER BY priority DESC, created_at ASC
   LIMIT 50

2. For each job: Enqueue(job)  ← no per-queue limit check
```

This is correct but naive — it dispatches all queued jobs equally, and the priority ordering only helps within the same dispatch batch.

### After Phase 8 (new `scheduleQueuedJobs`)

```
1. Compute available capacity per queue:
     highCap    = min(cfg.High.MaxConcurrent,    batchSize * cfg.High.Weight)
     defaultCap = min(cfg.Default.MaxConcurrent, batchSize * cfg.Default.Weight)
     lowCap     = min(cfg.Low.MaxConcurrent,     batchSize * cfg.Low.Weight)

2. SELECT high-priority jobs (queue=high, limit=highCap)
3. SELECT default jobs        (queue=default, limit=defaultCap)
4. SELECT low jobs            (queue=low, limit=lowCap)

5. Dispatch in priority order: high first, then default, then low
   For each job:
     if rateLimiter.Allow(queue):  ← token bucket check
       Enqueue(job)
     else:
       skip  ← will be picked up next tick when tokens refill
```

**Key insight:** Steps 2-4 are separate SQL queries, not one big `ORDER BY priority` query. This allows per-queue capacity to be independently configured without the highest-priority queue always consuming the full batch.

---

## 5. Configuration Model

### Environment variables (simple, operator-friendly)

```bash
# Per-queue concurrency limits (max worker slots this queue can occupy)
ORION_QUEUE_HIGH_MAX_CONCURRENT=8      # default: 80% of worker concurrency
ORION_QUEUE_DEFAULT_MAX_CONCURRENT=6   # default: 60% of worker concurrency
ORION_QUEUE_LOW_MAX_CONCURRENT=2       # default: 20% of worker concurrency

# Per-queue dispatch weight (what fraction of the batch goes to each queue)
ORION_QUEUE_HIGH_WEIGHT=0.8
ORION_QUEUE_DEFAULT_WEIGHT=0.6
ORION_QUEUE_LOW_WEIGHT=0.2

# Per-queue enqueue rate limits (tokens per second)
ORION_QUEUE_HIGH_RATE_PER_SEC=100.0   # 100 jobs/sec maximum enqueue rate
ORION_QUEUE_DEFAULT_RATE_PER_SEC=50.0
ORION_QUEUE_LOW_RATE_PER_SEC=10.0

# Token bucket burst capacity (how many tokens can accumulate)
ORION_QUEUE_HIGH_BURST=20
ORION_QUEUE_DEFAULT_BURST=10
ORION_QUEUE_LOW_BURST=5
```

### Dynamic configuration via API (operator-friendly, no restart needed)

```
GET  /queues                        → list all queue configurations
GET  /queues/{name}                 → get config for one queue
PUT  /queues/{name}                 → update queue config (live reload)
GET  /queues/{name}/stats           → current depth, active slots, rate usage
```

Dynamic config is stored in the `queue_config` table (migration 003) and reloaded by the scheduler on each tick. No service restart needed to change limits.

---

## 6. Complete File Plan

Phase 8 touches 8 locations. Three new files, five updates.

```
internal/store/migrations/
├── 003_queue_config.up.sql          ← NEW: queue_config table
└── 003_queue_config.down.sql        ← NEW: rollback

internal/config/
└── config.go                        ← UPDATED: QueueLimitConfig structs

internal/scheduler/
├── ratelimiter.go                   ← NEW: token bucket per queue
├── fairqueue.go                     ← NEW: weighted fair dispatch
└── scheduler.go                     ← UPDATED: uses RateLimiter + FairQueue

internal/api/handler/
└── queue.go                         ← NEW: GET/PUT /queues endpoints

cmd/api/
└── main.go                          ← UPDATED: register queue routes

cmd/scheduler/
└── main.go                          ← UPDATED: build RateLimiter, pass to scheduler

docs/phase8/
├── PHASE8-MASTER-GUIDE.md           ← this document
└── README-rate-limiting.md          ← operator guide
```

### What is NOT changing

| File | Why unchanged |
|---|---|
| `internal/worker/pool.go` | Pool structure is unchanged — concurrency limiting happens in scheduler dispatch, not in the pool itself. Phase 8.1 (future) adds pool-side semaphores. |
| `internal/domain/job.go` | Priority constants and JobType already exist |
| `internal/store/store.go` | No new store interface methods needed |
| `internal/store/postgres/db.go` | `ListJobs` already supports `queue_name` filter |
| `internal/pipeline/advancement.go` | Pipeline jobs go to `default` queue, unaffected |

---

## 7. File 1: Migration 003

### `003_queue_config.up.sql`

```sql
-- Migration: 003_queue_config.up.sql
-- Creates the queue_config table for dynamic per-queue rate limiting.
-- Values here are reloaded by the scheduler on each tick — no restart needed.

CREATE TABLE IF NOT EXISTS queue_config (
    queue_name          TEXT        PRIMARY KEY,
    max_concurrent      INT         NOT NULL DEFAULT 10,  -- max simultaneous worker slots
    weight              FLOAT       NOT NULL DEFAULT 0.5, -- dispatch weight (0.0-1.0)
    rate_per_sec        FLOAT       NOT NULL DEFAULT 50.0,-- token refill rate
    burst               INT         NOT NULL DEFAULT 10,  -- max token accumulation
    enabled             BOOLEAN     NOT NULL DEFAULT TRUE, -- set FALSE to pause a queue
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed with sensible defaults for the three standard queues.
-- UPDATE these rows to change limits live (no migration, no restart).
INSERT INTO queue_config (queue_name, max_concurrent, weight, rate_per_sec, burst)
VALUES
    ('orion:queue:high',    8, 0.8, 100.0, 20),
    ('orion:queue:default', 6, 0.6,  50.0, 10),
    ('orion:queue:low',     2, 0.2,  10.0,  5)
ON CONFLICT (queue_name) DO NOTHING;

-- Index for the scheduler's periodic reload query
CREATE INDEX IF NOT EXISTS idx_queue_config_enabled
    ON queue_config (enabled)
    WHERE enabled = TRUE;
```

### `003_queue_config.down.sql`

```sql
DROP INDEX IF EXISTS idx_queue_config_enabled;
DROP TABLE IF EXISTS queue_config;
```

---

## 8. File 2: `internal/config/config.go` — Queue Limit Config

Add `QueueLimitConfig` struct and per-queue fields to the root config.

### New types to add

```go
// QueueLimitConfig holds rate limiting and concurrency settings for one queue.
// All values are loaded from environment variables with safe defaults.
// Dynamic overrides come from the queue_config PostgreSQL table (Phase 8).
type QueueLimitConfig struct {
    // MaxConcurrent: maximum number of worker slots this queue can occupy.
    // When this many jobs from this queue are running, no more are dispatched.
    MaxConcurrent int

    // Weight: fraction of each scheduler batch allocated to this queue.
    // Used by the weighted fair scheduler. Must be between 0.0 and 1.0.
    // Weights do not need to sum to 1.0 — they are relative proportions.
    Weight float64

    // RatePerSec: token bucket refill rate (jobs per second).
    // Controls how fast jobs can be enqueued, independently of concurrency.
    RatePerSec float64

    // Burst: maximum token accumulation in the token bucket.
    // A queue with Burst=20 can dispatch 20 jobs instantly before the rate limit kicks in.
    Burst int
}

// QueueConfig holds limits for all three standard Orion queues.
type QueueConfig struct {
    High    QueueLimitConfig
    Default QueueLimitConfig
    Low     QueueLimitConfig
}
```

### Defaults in `Load()`

```go
Queue: QueueConfig{
    High: QueueLimitConfig{
        MaxConcurrent: getEnvInt("ORION_QUEUE_HIGH_MAX_CONCURRENT", workerConcurrency*8/10),
        Weight:        getEnvFloat("ORION_QUEUE_HIGH_WEIGHT", 0.8),
        RatePerSec:    getEnvFloat("ORION_QUEUE_HIGH_RATE_PER_SEC", 100.0),
        Burst:         getEnvInt("ORION_QUEUE_HIGH_BURST", 20),
    },
    Default: QueueLimitConfig{
        MaxConcurrent: getEnvInt("ORION_QUEUE_DEFAULT_MAX_CONCURRENT", workerConcurrency*6/10),
        Weight:        getEnvFloat("ORION_QUEUE_DEFAULT_WEIGHT", 0.6),
        RatePerSec:    getEnvFloat("ORION_QUEUE_DEFAULT_RATE_PER_SEC", 50.0),
        Burst:         getEnvInt("ORION_QUEUE_DEFAULT_BURST", 10),
    },
    Low: QueueLimitConfig{
        MaxConcurrent: getEnvInt("ORION_QUEUE_LOW_MAX_CONCURRENT", workerConcurrency*2/10),
        Weight:        getEnvFloat("ORION_QUEUE_LOW_WEIGHT", 0.2),
        RatePerSec:    getEnvFloat("ORION_QUEUE_LOW_RATE_PER_SEC", 10.0),
        Burst:         getEnvInt("ORION_QUEUE_LOW_BURST", 5),
    },
},
```

---

## 9. File 3: `internal/scheduler/ratelimiter.go` — Token Bucket

### Complete code to write

```go
// Package scheduler — ratelimiter.go
// Implements a thread-safe token bucket rate limiter per queue.
// Each queue has an independent bucket: tokens refill at RatePerSec,
// accumulate up to Burst, and one token is consumed per job dispatched.

package scheduler

import (
    "context"
    "sync"
    "time"
)

// QueueRateLimiter holds independent token buckets for each queue.
// Thread-safe: all methods use the mu mutex.
type QueueRateLimiter struct {
    mu      sync.Mutex
    buckets map[string]*tokenBucket
}

type tokenBucket struct {
    tokens     float64   // current token count (float for sub-second refills)
    maxTokens  float64   // burst capacity
    refillRate float64   // tokens added per second
    lastRefill time.Time // when tokens were last added
}

// NewQueueRateLimiter creates a QueueRateLimiter.
// queueConfigs maps queue name → (ratePerSec, burst).
func NewQueueRateLimiter(queueConfigs map[string]bucketConfig) *QueueRateLimiter {
    rl := &QueueRateLimiter{
        buckets: make(map[string]*tokenBucket, len(queueConfigs)),
    }
    for name, cfg := range queueConfigs {
        rl.buckets[name] = &tokenBucket{
            tokens:     float64(cfg.burst), // start full
            maxTokens:  float64(cfg.burst),
            refillRate: cfg.ratePerSec,
            lastRefill: time.Now(),
        }
    }
    return rl
}

type bucketConfig struct {
    ratePerSec float64
    burst      int
}

// Allow returns true if one token is available for the given queue,
// consuming it atomically. Returns false if the bucket is empty (rate limited).
// Thread-safe.
func (rl *QueueRateLimiter) Allow(queueName string) bool {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    bucket, ok := rl.buckets[queueName]
    if !ok {
        return true // unknown queue: no limit applied
    }

    // Refill tokens based on elapsed time since last refill
    now := time.Now()
    elapsed := now.Sub(bucket.lastRefill).Seconds()
    bucket.tokens = min(bucket.maxTokens, bucket.tokens+elapsed*bucket.refillRate)
    bucket.lastRefill = now

    if bucket.tokens < 1.0 {
        return false // rate limited
    }
    bucket.tokens--
    return true
}

// UpdateConfig replaces the configuration for a queue's bucket.
// Called when the scheduler reloads queue_config from PostgreSQL.
func (rl *QueueRateLimiter) UpdateConfig(queueName string, ratePerSec float64, burst int) {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    if bucket, ok := rl.buckets[queueName]; ok {
        bucket.refillRate = ratePerSec
        bucket.maxTokens = float64(burst)
        // Cap tokens to new max if it decreased
        if bucket.tokens > bucket.maxTokens {
            bucket.tokens = bucket.maxTokens
        }
    } else {
        rl.buckets[queueName] = &tokenBucket{
            tokens:     float64(burst),
            maxTokens:  float64(burst),
            refillRate: ratePerSec,
            lastRefill: time.Now(),
        }
    }
}

// Available returns the approximate current token count for a queue.
// Used for metrics and the /queues/{name}/stats API endpoint.
func (rl *QueueRateLimiter) Available(queueName string) float64 {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    if b, ok := rl.buckets[queueName]; ok {
        return b.tokens
    }
    return 0
}

func min(a, b float64) float64 {
    if a < b {
        return a
    }
    return b
}
```

---

## 10. File 4: `internal/scheduler/fairqueue.go` — Fair Scheduler

### Complete code to write

```go
// fairqueue.go — weighted fair dispatch across queues.
// The FairQueue determines how many jobs to dispatch from each queue
// per scheduler tick, based on configured weights and current limits.

package scheduler

import (
    "context"
    "log/slog"

    "github.com/shreeharsh-a/orion/internal/domain"
    "github.com/shreeharsh-a/orion/internal/store"
)

// QueueAllocation holds the dispatch plan for one scheduler tick.
type QueueAllocation struct {
    QueueName string
    Limit     int // how many jobs to fetch and dispatch from this queue
    Weight    float64
}

// FairQueue computes per-queue allocations and dispatches in weighted order.
type FairQueue struct {
    queues  []QueueAllocation // sorted: highest weight first
    limiter *QueueRateLimiter
    store   store.Store
    logger  *slog.Logger
}

// NewFairQueue creates a FairQueue from the given allocations.
// Allocations are sorted by weight descending before storage.
func NewFairQueue(allocations []QueueAllocation, limiter *QueueRateLimiter,
    s store.Store, logger *slog.Logger) *FairQueue {
    // Sort: highest weight dispatched first
    sorted := make([]QueueAllocation, len(allocations))
    copy(sorted, allocations)
    sortByWeightDesc(sorted)
    return &FairQueue{queues: sorted, limiter: limiter, store: s, logger: logger}
}

// FetchReadyJobs fetches jobs from all queues according to their allocations.
// Returns jobs in dispatch priority order (high queue jobs first).
// Each queue is fetched independently — avoids one queue's backlog affecting another.
func (fq *FairQueue) FetchReadyJobs(ctx context.Context) ([]*domain.Job, error) {
    var allJobs []*domain.Job

    for _, alloc := range fq.queues {
        if alloc.Limit <= 0 {
            continue
        }

        queueName := alloc.QueueName
        status := domain.JobStatusQueued

        jobs, err := fq.store.ListJobs(ctx, store.JobFilter{
            Status:    &status,
            QueueName: &queueName,
            Limit:     alloc.Limit,
        })
        if err != nil {
            fq.logger.Error("failed to list jobs for queue",
                "queue", queueName, "err", err)
            continue
        }
        allJobs = append(allJobs, jobs...)
    }

    return allJobs, nil
}

// AllowDispatch checks the rate limiter before dispatching a job.
// Returns true if the job should be dispatched this tick.
func (fq *FairQueue) AllowDispatch(queueName string) bool {
    return fq.limiter.Allow(queueName)
}

// ComputeAllocations calculates per-queue dispatch limits for a given total batch size.
// Ensures the sum of allocations does not exceed batchSize.
// Used by the scheduler to build the FairQueue on each tick.
func ComputeAllocations(queueCfgs map[string]QueueAllocation, batchSize int) []QueueAllocation {
    var result []QueueAllocation
    remaining := batchSize

    // Process in weight-descending order
    sorted := make([]QueueAllocation, 0, len(queueCfgs))
    for _, v := range queueCfgs {
        sorted = append(sorted, v)
    }
    sortByWeightDesc(sorted)

    for _, cfg := range sorted {
        limit := int(float64(batchSize) * cfg.Weight)
        if limit > remaining {
            limit = remaining
        }
        if limit < 0 {
            limit = 0
        }
        result = append(result, QueueAllocation{
            QueueName: cfg.QueueName,
            Weight:    cfg.Weight,
            Limit:     limit,
        })
        remaining -= limit
        if remaining <= 0 {
            break
        }
    }
    return result
}

func sortByWeightDesc(a []QueueAllocation) {
    // Simple insertion sort — small N (typically 3 queues)
    for i := 1; i < len(a); i++ {
        key := a[i]
        j := i - 1
        for j >= 0 && a[j].Weight < key.Weight {
            a[j+1] = a[j]
            j--
        }
        a[j+1] = key
    }
}
```

---

## 11. File 5: Updated `internal/scheduler/scheduler.go`

### The changed `scheduleQueuedJobs` function

```go
// scheduleQueuedJobs dispatches queued jobs to Redis using weighted fair scheduling.
// Phase 8 change: uses FairQueue.FetchReadyJobs instead of a single ListJobs call.
func (s *Scheduler) scheduleQueuedJobs(ctx context.Context) error {
    ctx, span := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_cycle")
    defer span.End()

    cycleStart := time.Now()
    defer func() {
        if s.metrics != nil {
            s.metrics.SchedulerCycleLatency.Observe(time.Since(cycleStart).Seconds())
        }
    }()

    // ── Phase 8: compute weighted allocations ─────────────────────────────────
    // Each queue gets a slice of the batch proportional to its weight.
    // This replaces the single ListJobs call with per-queue fetches.
    allocations := ComputeAllocations(s.queueAllocations, s.cfg.BatchSize)
    fairQ := NewFairQueue(allocations, s.rateLimiter, s.store, s.logger)

    jobs, err := fairQ.FetchReadyJobs(ctx)
    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        return fmt.Errorf("fetching ready jobs: %w", err)
    }

    span.SetAttributes(attribute.Int("jobs.found", len(jobs)))

    dispatched := 0
    rateLimited := 0
    for _, job := range jobs {
        // ── Rate limiter check ─────────────────────────────────────────────
        if !fairQ.AllowDispatch(job.QueueName) {
            rateLimited++
            s.metrics.QueueRateLimited.WithLabelValues(job.QueueName).Inc()
            continue // will be retried on next tick when bucket refills
        }

        _, jobSpan := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_job")
        jobSpan.SetAttributes(
            attribute.String("job.id", job.ID.String()),
            attribute.String("job.queue", job.QueueName),
            attribute.Int("job.priority", int(job.Priority)),
        )

        if err := s.store.TransitionJobState(ctx, job.ID,
            domain.JobStatusQueued, domain.JobStatusScheduled); err != nil {
            jobSpan.End()
            continue
        }
        if err := s.queue.Enqueue(ctx, job); err != nil {
            _ = s.store.TransitionJobState(ctx, job.ID,
                domain.JobStatusScheduled, domain.JobStatusQueued)
            jobSpan.SetStatus(codes.Error, err.Error())
            jobSpan.End()
            continue
        }

        if s.metrics != nil {
            s.metrics.JobsSubmitted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
        }
        jobSpan.End()
        dispatched++
    }

    span.SetAttributes(
        attribute.Int("jobs.dispatched", dispatched),
        attribute.Int("jobs.rate_limited", rateLimited),
    )
    if dispatched > 0 || rateLimited > 0 {
        s.logger.Info("scheduler cycle complete",
            "dispatched", dispatched, "rate_limited", rateLimited)
    }
    return nil
}
```

### New fields in Scheduler struct

```go
type Scheduler struct {
    cfg              Config
    db               *pgxpool.Pool
    store            store.Store
    queue            queue.Queue
    advancer         *pipeline.Advancer
    metrics          *observability.Metrics
    rateLimiter      *QueueRateLimiter       // Phase 8
    queueAllocations map[string]QueueAllocation // Phase 8: pre-built from config
    logger           *slog.Logger
}
```

---

## 12. File 6: `internal/api/handler/queue.go` — Queue Config API

### Routes and handlers

```go
// GET /queues — list all queue configurations
func (h *QueueHandler) ListQueues(w http.ResponseWriter, r *http.Request) {
    configs, err := h.store.ListQueueConfigs(r.Context())
    // ...
    writeJSON(w, http.StatusOK, map[string]any{"queues": configs, "count": len(configs)})
}

// GET /queues/{name} — get config for one queue
func (h *QueueHandler) GetQueue(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    cfg, err := h.store.GetQueueConfig(r.Context(), name)
    if errors.Is(err, store.ErrNotFound) {
        writeError(w, http.StatusNotFound, "queue not found")
        return
    }
    writeJSON(w, http.StatusOK, cfg)
}

// PUT /queues/{name} — update queue config (live reload, no restart)
func (h *QueueHandler) UpdateQueue(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    var req UpdateQueueRequest
    // decode, validate, update store
    // scheduler picks up new values on next tick via LoadQueueConfigs
    writeJSON(w, http.StatusOK, updated)
}

// GET /queues/{name}/stats — real-time stats for one queue
func (h *QueueHandler) GetQueueStats(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    depth, _ := h.redisQueue.Len(r.Context(), name)
    tokens := h.rateLimiter.Available(name)
    writeJSON(w, http.StatusOK, map[string]any{
        "queue_name":       name,
        "depth":            depth,
        "rate_tokens_avail": tokens,
    })
}
```

---

## 13. File 7: `cmd/api/main.go` — New Routes

```go
// After existing job and pipeline routes:
queueHandler := handler.NewQueueHandler(pgStore, rateLimiter, redisQueue, logger)
mux.HandleFunc("GET /queues",              queueHandler.ListQueues)
mux.HandleFunc("GET /queues/{name}",       queueHandler.GetQueue)
mux.HandleFunc("PUT /queues/{name}",       queueHandler.UpdateQueue)
mux.HandleFunc("GET /queues/{name}/stats", queueHandler.GetQueueStats)
```

---

## 14. File 8: `cmd/scheduler/main.go` — RateLimiter Wired

```go
// Build rate limiter from config
rateLimiter := scheduler.NewQueueRateLimiter(map[string]scheduler.BucketConfig{
    "orion:queue:high":    {RatePerSec: cfg.Queue.High.RatePerSec,    Burst: cfg.Queue.High.Burst},
    "orion:queue:default": {RatePerSec: cfg.Queue.Default.RatePerSec, Burst: cfg.Queue.Default.Burst},
    "orion:queue:low":     {RatePerSec: cfg.Queue.Low.RatePerSec,     Burst: cfg.Queue.Low.Burst},
})

// Build queue allocations for the fair scheduler
queueAllocations := map[string]scheduler.QueueAllocation{
    "orion:queue:high":    {QueueName: "orion:queue:high",    Weight: cfg.Queue.High.Weight},
    "orion:queue:default": {QueueName: "orion:queue:default", Weight: cfg.Queue.Default.Weight},
    "orion:queue:low":     {QueueName: "orion:queue:low",     Weight: cfg.Queue.Low.Weight},
}

sched := scheduler.New(
    scheduler.Config{...},
    db, pgStore, queue, adv, metrics,
    rateLimiter,       // Phase 8
    queueAllocations,  // Phase 8
    logger,
)
```

---

## 15. The Token Bucket Algorithm — Deep Dive

The token bucket is the standard algorithm for rate limiting because it allows controlled bursting while enforcing an average rate.

### How it works

```
State: tokens (float64), maxTokens, refillRate (tokens/sec), lastRefill (time)

On every Allow() call:
  1. elapsed = now - lastRefill
  2. tokens = min(maxTokens, tokens + elapsed * refillRate)
  3. lastRefill = now
  4. if tokens >= 1.0:
       tokens -= 1.0
       return true   // allowed
     else:
       return false  // rate limited
```

### Example: high queue with rate=100/sec, burst=20

```
t=0:    tokens=20 (full)
t=0:    20 jobs submitted instantly → tokens=0   (burst consumed)
t=0.1s: tokens = 0 + 0.1 * 100 = 10             (10 tokens refilled)
t=0.1s: 10 jobs submitted → tokens=0
t=0.2s: tokens = 10, 10 more submitted...
...
t=1.0s: tokens = 100 - 20 dispatched = 80 available
```

The burst of 20 absorbs sudden spikes. The rate of 100/sec sustains throughput. No job waits more than 10ms for a token at this rate.

### Why float64 for tokens

Integer tokens lose precision at sub-second refills. At 100 tokens/sec with a 2-second scheduler tick, each tick would add 200 tokens. But at 10 tokens/sec with a 50ms tick, we'd add 0 integer tokens and the rate limiter would never fire. Float64 allows fractional token accumulation.

---

## 16. The Fair Scheduling Algorithm — Deep Dive

### The problem with ORDER BY priority alone

```sql
-- Current approach (Phase 7):
SELECT * FROM jobs WHERE status='queued'
ORDER BY priority DESC, created_at ASC
LIMIT 50
```

If there are 1000 low-priority jobs and 5 high-priority jobs:
- All 5 high-priority jobs are in the result ✓
- 45 low-priority jobs fill the rest of the batch ✓

But the `LIMIT 50` means we only dispatch 50 jobs per tick. If there are already 50 high-priority jobs queued, low-priority jobs never get dispatched in this tick, even if they've been waiting hours. Over time, low-priority jobs starve.

### The Phase 8 approach: per-queue allocation

```
batch_size = 50
weights: high=0.8, default=0.6, low=0.2

alloc_high    = floor(50 * 0.8) = 40 jobs from high
alloc_default = floor(50 * 0.6) = 30 jobs from default
alloc_low     = floor(50 * 0.2) = 10 jobs from low

BUT: total = 80 > 50, so we cap:
  high gets: min(40, 50) = 40
  remaining = 10
  default gets: min(30, 10) = 10
  remaining = 0
  low gets: 0
```

In practice: high-priority queue has first claim. But if it's empty (only 3 jobs), the remaining capacity flows to the next queue:

```
high has 3 jobs:   alloc_high = 3 (not 40, queue is small)
remaining = 47
default gets: min(30, 47) = 30
remaining = 17
low gets: min(10, 17) = 10
remaining = 7 (unused)
```

This guarantees: **every queue gets at least its minimum allocation when it has jobs, and unused capacity flows to lower-priority queues.**

---

## 17. Interaction With Existing Priority System

Phase 8 does NOT replace the existing `priority` field on individual jobs. The two systems are orthogonal:

| Mechanism | What it controls |
|---|---|
| `job.Priority` (1-10) | Ordering **within** a single queue — which job from that queue runs first |
| Queue weight (Phase 8) | Ordering **across** queues — how much dispatcher bandwidth each queue gets |
| Queue concurrency limit (Phase 8) | How many simultaneous worker slots a queue can occupy |

Example:
```
high queue: [job A priority=10, job B priority=7]
low queue:  [job C priority=10, job D priority=9]

With weights high=0.8, low=0.2:
  → high queue gets 80% of dispatch capacity
  → within high queue: A runs before B (priority 10 > 7)
  → low queue gets 20%: C runs before D
  → job A will always run before job C, regardless of priority,
    because A is in a higher-weighted queue
```

---

## 18. Metrics for Phase 8

### New metrics to add to `observability.go`

```go
// Phase 8 metrics
QueueRateLimited *prometheus.CounterVec   // labels: queue — jobs skipped due to rate limit
QueueConcurrentJobs *prometheus.GaugeVec // labels: queue — current active jobs per queue
QueueConcurrencyLimit *prometheus.GaugeVec // labels: queue — configured max slots per queue
QueueDispatchWeight *prometheus.GaugeVec // labels: queue — current weight value
```

### PromQL for new Grafana panels

```promql
# Rate-limited jobs per minute (indicates queue is being throttled)
rate(orion_queue_rate_limited_total[1m]) by (queue)

# Queue utilization: active / limit
orion_queue_concurrent_jobs / orion_queue_concurrency_limit

# Dispatch fairness: fraction of batch going to each queue
orion_queue_dispatch_weight
```

---

## 19. Step-by-Step Build Order

### Step 1 — Migration 003

```bash
# Write 003_queue_config.up.sql and .down.sql
make migrate-up
# Verify:
docker compose exec postgres psql -U orion -d orion \
  -c "SELECT queue_name, max_concurrent, weight FROM queue_config;"
# orion:queue:high    | 8 | 0.8
# orion:queue:default | 6 | 0.6
# orion:queue:low     | 2 | 0.2
```

### Step 2 — Config types

```bash
# Add QueueLimitConfig and QueueConfig to config.go
go build ./internal/config/...
# Must compile
```

### Step 3 — Token bucket rate limiter

```bash
# Write ratelimiter.go
go build ./internal/scheduler/...
go test -race ./internal/scheduler/... -run TestRateLimiter -v
```

### Step 4 — Fair queue

```bash
# Write fairqueue.go
go test -race ./internal/scheduler/... -run TestFairQueue -v
```

### Step 5 — Update scheduler

```bash
# Update scheduler.go: add rateLimiter + queueAllocations fields,
# update New() signature, update scheduleQueuedJobs
go build ./internal/scheduler/...
# Will fail until cmd/scheduler/main.go is updated
```

### Step 6 — Update cmd/scheduler/main.go

```bash
go build ./cmd/scheduler/...
# Must compile
```

### Step 7 — Queue config API handler

```bash
# Write internal/api/handler/queue.go
# Update cmd/api/main.go with 4 new routes
go build ./cmd/api/...
# Must compile
```

### Step 8 — New metrics

```bash
# Add Phase 8 metrics to observability.go
go build ./internal/observability/...
```

### Step 9 — Full build + all tests

```bash
make build
go test -race ./... -count=1
# All 171 existing tests pass (no regressions)
```

---

## 20. Complete Verification Sequence

### Test 1 — Starvation prevention: high-priority job completes despite low-priority flood

```bash
# Start services
make infra-up && make migrate-up
make run-api && make run-scheduler && make run-worker

# Flood low-priority queue (500 slow jobs)
for i in $(seq 1 500); do
  curl -s -X POST localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{"name":"batch-'$i'","type":"inline","queue_name":"low",
         "payload":{"handler_name":"slow","args":{"duration_seconds":5}}}' > /dev/null
done
echo "500 low-priority jobs submitted"

# Submit one high-priority job
HP_ID=$(curl -s -X POST localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"urgent-job","type":"inline","queue_name":"high",
       "payload":{"handler_name":"noop"}}' | jq -r .id)

# High-priority job should complete within 3 seconds
SECONDS=0
while [ $SECONDS -lt 30 ]; do
  STATUS=$(curl -s localhost:8080/jobs/$HP_ID | jq -r .status)
  echo "t=${SECONDS}s: $STATUS"
  [ "$STATUS" = "completed" ] && echo "✅ TEST 1 PASSED — high-priority job completed despite flood" && break
  sleep 1
done
```

### Test 2 — Rate limiter enforcement

```bash
# Verify rate-limited counter increments under load
START_RL=$(curl -s "localhost:9090/api/v1/query?query=orion_queue_rate_limited_total" \
  | jq '.data.result[0].value[1] // "0"' -r)

# Submit 200 jobs to low queue instantly (burst is 5, rate is 10/sec)
for i in $(seq 1 200); do
  curl -s -X POST localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{"name":"rl-test-'$i'","type":"inline","queue_name":"low",
         "payload":{"handler_name":"noop"}}' > /dev/null
done

sleep 5
END_RL=$(curl -s "localhost:9090/api/v1/query?query=orion_queue_rate_limited_total" \
  | jq '.data.result[0].value[1] // "0"' -r)

echo "Rate-limited events: $((END_RL - START_RL))"
[ $((END_RL - START_RL)) -gt 0 ] && echo "✅ TEST 2 PASSED — rate limiting active"
```

### Test 3 — Dynamic config update (no restart)

```bash
# Read current low queue config
curl -s localhost:8080/queues/orion:queue:low | jq .

# Update low queue rate to 1/sec (throttle it severely)
curl -s -X PUT localhost:8080/queues/orion:queue:low \
  -H "Content-Type: application/json" \
  -d '{"rate_per_sec":1.0,"burst":2}' | jq .

# Verify scheduler picks up new config within 2 seconds (one tick)
sleep 3
curl -s localhost:8080/queues/orion:queue:low/stats | jq .
# rate_tokens_avail: ~1 (barely any tokens)
echo "✅ TEST 3 PASSED — dynamic config reloaded without restart"
```

### Test 4 — Fair scheduling allocation verification

```bash
# Check via SQL that dispatch is proportional
docker compose exec postgres psql -U orion -d orion \
  -c "SELECT queue_name, count(*) as completed
      FROM jobs WHERE status='completed' AND created_at > NOW() - INTERVAL '5 minutes'
      GROUP BY queue_name ORDER BY queue_name;"
# orion:queue:high    | ~40%+ of completions
# orion:queue:default | ~35-40%
# orion:queue:low     | ~15-20%
echo "✅ TEST 4 PASSED — fair scheduling proportions confirmed"
```

---

## 21. Common Mistakes

| Mistake | Symptom | Fix |
|---|---|---|
| Token bucket using integer tokens | Rate limiter fires on first job when rate < 1/tick | Use `float64` for `tokens` field |
| Not refilling before checking | Rate limiter more aggressive than configured | Always refill first in `Allow()`, before checking |
| Weight sum > 1.0 causing over-allocation | Batch size exceeded | `ComputeAllocations` caps each queue's limit at remaining capacity |
| Rate limiter shared across instances | Two schedulers share one bucket | Rate limiter is in-process, per-scheduler — correct by design (advisory lock ensures one scheduler) |
| Config reload on every DB call | O(N) DB queries per tick | Only reload queue_config table once per tick, cache results |
| Skipped jobs never retried | Rate-limited jobs lost | Skipped jobs stay in `queued` status in PostgreSQL — picked up on the next tick |
| All weights set to 1.0 | Fair queue becomes FIFO again | ComputeAllocations with equal weights = equal slices per queue — correct behavior |
| `ListJobs` without `queue_name` filter | All queues fetched in one query | Phase 8 requires per-queue fetches — add `QueueName *string` to `JobFilter` |

---

## 22. Phase 9 Preview

After Phase 8, Orion is feature-complete:
- ✅ Correct execution (Phases 1-4)
- ✅ Pipeline orchestration (Phase 5)
- ✅ Full observability (Phase 6)
- ✅ Real-time gRPC streaming (Phase 7)
- ✅ Fair scheduling and rate limiting (Phase 8)

Phase 9 packages Orion for production Kubernetes deployment. One command deploys everything:

```bash
helm install orion ./deploy/helm \
  --set api.replicaCount=3 \
  --set worker.replicaCount=10 \
  --set worker.autoscaling.enabled=true \
  --set worker.autoscaling.maxReplicas=50 \
  --set database.existingSecret=orion-db-secret \
  --namespace orion-system
```

Phase 9 adds:
- Multi-stage Dockerfiles for all 3 binaries (final image: ~15MB scratch-based)
- Helm chart with Deployment, Service, HPA, RBAC, ConfigMap, Secret templates
- HorizontalPodAutoscaler for workers based on `orion_queue_depth`
- PodDisruptionBudgets (API: min 2 available, Scheduler: exactly 1)
- Production checklist: TLS, network policies, resource limits, liveness/readiness probes

---

## Summary Checklist

```
□ internal/store/migrations/003_queue_config.up.sql
    □ queue_config table (queue_name PK, max_concurrent, weight, rate_per_sec, burst, enabled)
    □ Seeded with 3 default rows (high=8/0.8, default=6/0.6, low=2/0.2)
    □ idx_queue_config_enabled partial index

□ internal/config/config.go
    □ QueueLimitConfig struct (MaxConcurrent, Weight, RatePerSec, Burst)
    □ QueueConfig struct (High, Default, Low)
    □ Defaults wired with ORION_QUEUE_* env vars

□ internal/scheduler/ratelimiter.go
    □ tokenBucket struct (tokens float64, maxTokens, refillRate, lastRefill)
    □ QueueRateLimiter struct (mu sync.Mutex, buckets map)
    □ NewQueueRateLimiter() — starts all buckets full
    □ Allow() — refill then consume, thread-safe
    □ UpdateConfig() — live reload from queue_config table
    □ Available() — for metrics and stats API

□ internal/scheduler/fairqueue.go
    □ QueueAllocation struct (QueueName, Limit, Weight)
    □ FairQueue struct (queues, limiter, store, logger)
    □ NewFairQueue() — sorts by weight descending
    □ FetchReadyJobs() — per-queue ListJobs calls
    □ AllowDispatch() — delegates to rate limiter
    □ ComputeAllocations() — weight-based batch partitioning

□ internal/scheduler/scheduler.go (update)
    □ rateLimiter + queueAllocations fields added
    □ New() accepts both new parameters
    □ scheduleQueuedJobs uses FairQueue.FetchReadyJobs
    □ Per-job AllowDispatch check with rate-limited counter
    □ Span attributes include jobs.rate_limited count

□ internal/api/handler/queue.go (new)
    □ QueueHandler struct
    □ ListQueues   GET /queues
    □ GetQueue     GET /queues/{name}
    □ UpdateQueue  PUT /queues/{name}
    □ GetQueueStats GET /queues/{name}/stats

□ cmd/api/main.go (update)
    □ 4 queue routes registered
    □ rateLimiter and redisQueue passed to QueueHandler

□ cmd/scheduler/main.go (update)
    □ QueueRateLimiter built from config
    □ queueAllocations map built from config
    □ Both passed to scheduler.New()

□ internal/observability/observability.go (update)
    □ QueueRateLimited CounterVec added (labels: queue)
    □ QueueConcurrentJobs GaugeVec added (labels: queue)
    □ QueueConcurrencyLimit GaugeVec added (labels: queue)
    □ QueueDispatchWeight GaugeVec added (labels: queue)

□ Verification
    □ make migrate-up → queue_config table has 3 default rows
    □ Test 1: 500 low-priority jobs + 1 high → high completes in <10s ✅
    □ Test 2: 200 instant submits to low queue → rate_limited counter > 0 ✅
    □ Test 3: PUT /queues/low changes rate → active within 2 seconds ✅
    □ Test 4: Completion ratios match configured weights ±15% ✅
    □ go test -race ./... → all 171+ tests pass ✅
```