# Phase 8 — Unstaged Code Changes

Changes not yet staged for commit as of 2026-05-04.

---

## Summary

| Type | Files |
|------|-------|
| Modified | `cmd/api/main.go` |
| Modified | `cmd/scheduler/main.go` |
| Modified | `internal/api/grpc/server_test.go` |
| Modified | `internal/api/handler/job_test.go` |
| Modified | `internal/api/handler/pipeline_test.go` |
| Modified | `internal/config/config.go` |
| Modified | `internal/observability/observability.go` |
| Modified | `internal/pipeline/advancement_test.go` |
| Modified | `internal/scheduler/scheduler.go` |
| Modified | `internal/store/postgres/db.go` |
| Modified | `internal/store/store.go` |
| **New (untracked)** | `internal/api/handler/queue.go` |
| **New (untracked)** | `internal/scheduler/fairqueue.go` |
| **New (untracked)** | `internal/scheduler/ratelimiter.go` |
| **New (untracked)** | `internal/store/migrations/003_queue_config.up.sql` |
| **New (untracked)** | `internal/store/migrations/003_queue_config.down.sql` |

---

## `internal/store/store.go`

Added `QueueConfig` struct, `QueueConfigStore` interface, and embedded it into `Store`.

```diff
+// QueueConfig is the runtime configuration for one queue.
+type QueueConfig struct {
+    QueueName     string    `json:"queue_name"`
+    MaxConcurrent int       `json:"max_concurrent"`
+    Weight        float64   `json:"weight"`
+    RatePerSec    float64   `json:"rate_per_sec"`
+    Burst         int       `json:"burst"`
+    Enabled       bool      `json:"enabled"`
+    UpdatedAt     time.Time `json:"updated_at"`
+}
+
+type QueueConfigStore interface {
+    ListQueueConfigs(ctx context.Context) ([]*QueueConfig, error)
+    GetQueueConfig(ctx context.Context, queueName string) (*QueueConfig, error)
+    UpsertQueueConfig(ctx context.Context, cfg *QueueConfig) (*QueueConfig, error)
+}

 type Store interface {
     JobStore
     ExecutionStore
     WorkerStore
-    PipelineStore
+    PipelineStore
+    QueueConfigStore   // added Phase 8
 }
```

---

## `internal/store/postgres/db.go`

Added `ListQueueConfigs`, `GetQueueConfig`, `UpsertQueueConfig` implementations.

```diff
+func (db *DB) ListQueueConfigs(ctx context.Context) ([]*store.QueueConfig, error) {
+    const q = `SELECT queue_name, max_concurrent, weight, rate_per_sec, burst, enabled, updated_at
+               FROM queue_config ORDER BY queue_name ASC`
+    // ... scan rows ...
+}
+
+func (db *DB) GetQueueConfig(ctx context.Context, queueName string) (*store.QueueConfig, error) {
+    // returns store.ErrNotFound on pgx.ErrNoRows
+}
+
+func (db *DB) UpsertQueueConfig(ctx context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) {
+    // INSERT ... ON CONFLICT (queue_name) DO UPDATE SET ... RETURNING ...
+}
```

---

## `internal/config/config.go`

Added `QueueLimitConfig` and `QueueConfig` types; loaded from env vars with defaults derived from `Worker.Concurrency`.

```diff
+type QueueLimitConfig struct {
+    MaxConcurrent int
+    Weight        float64
+    RatePerSec    float64
+    Burst         int
+}
+
+type QueueConfig struct {
+    High    QueueLimitConfig
+    Default QueueLimitConfig
+    Low     QueueLimitConfig
+}

 type Config struct {
     ...
+    Queue QueueConfig
 }
```

Defaults in `Load()`:

| Queue   | MaxConcurrent | Weight | RatePerSec | Burst |
|---------|--------------|--------|------------|-------|
| high    | 80% of concurrency | 0.8 | 100 | 20 |
| default | 60% of concurrency | 0.6 | 50  | 10 |
| low     | 20% of concurrency | 0.2 | 10  | 5  |

---

## `internal/observability/observability.go`

Added 4 new Phase 8 Prometheus metrics.

```diff
+QueueRateLimited      *prometheus.CounterVec  // labels: queue
+QueueConcurrentJobs   *prometheus.GaugeVec    // labels: queue
+QueueConcurrencyLimit *prometheus.GaugeVec    // labels: queue
+QueueDispatchWeight   *prometheus.GaugeVec    // labels: queue
```

Metric names:
- `orion_queue_rate_limited_total`
- `orion_queue_concurrent_jobs`
- `orion_queue_concurrency_limit`
- `orion_queue_dispatch_weight`

---

## `internal/scheduler/scheduler.go`

Added `rateLimiter` and `queueAllocations` fields; wired into `scheduleQueuedJobs`.

```diff
 type Scheduler struct {
     ...
+    rateLimiter      *QueueRateLimiter
+    queueAllocations map[string]QueueAllocation
 }

 func New(...,
+    rateLimiter *QueueRateLimiter,
+    queueAllocations map[string]QueueAllocation,
     logger *slog.Logger,
 ) *Scheduler
```

`scheduleQueuedJobs` now:
1. Uses `FairQueue.FetchReadyJobs` when allocations are configured (Phase 8 path)
2. Falls back to `ListJobs` when not configured (backward-compatible for tests)
3. Checks `rateLimiter.Allow(job.QueueName)` per job; skips and increments `QueueRateLimited` counter on exhaustion
4. Logs `dispatched` + `rate_limited` counts per cycle

---

## `cmd/scheduler/main.go`

Wired `QueueRateLimiter` and `queueAllocations` from config; publishes initial gauge values to Prometheus; passes both to `scheduler.New`.

```diff
+rateLimiter := scheduler.NewQueueRateLimiter(map[string]scheduler.BucketConfig{
+    "orion:queue:high":    {RatePerSec: cfg.Queue.High.RatePerSec, Burst: cfg.Queue.High.Burst},
+    "orion:queue:default": {RatePerSec: cfg.Queue.Default.RatePerSec, Burst: cfg.Queue.Default.Burst},
+    "orion:queue:low":     {RatePerSec: cfg.Queue.Low.RatePerSec, Burst: cfg.Queue.Low.Burst},
+})
+queueAllocations := map[string]scheduler.QueueAllocation{
+    "orion:queue:high":    {QueueName: "orion:queue:high", Weight: cfg.Queue.High.Weight},
+    "orion:queue:default": {QueueName: "orion:queue:default", Weight: cfg.Queue.Default.Weight},
+    "orion:queue:low":     {QueueName: "orion:queue:low", Weight: cfg.Queue.Low.Weight},
+}

 sched := scheduler.New(
     ...,
-    metrics,
+    metrics, rateLimiter, queueAllocations,
     logger,
 )
```

---

## `cmd/api/main.go`

Added Redis client init, `QueueRateLimiter`, and `/queues` route registration.

```diff
+redisClient := redis.NewClient(&redis.Options{...})
+redisQ, err := redisqueue.New(redisClient, metrics, logger)
+// nil-safe: if init fails, /queues stats show depth=0

+rateLimiter := scheduler.NewQueueRateLimiter(map[string]scheduler.BucketConfig{...})

+queueHandler := handler.NewQueueHandler(pgStore, rateLimiter, redisQ, logger)
+mux.HandleFunc("GET /queues",             queueHandler.ListQueues)
+mux.HandleFunc("GET /queues/{name}",      queueHandler.GetQueue)
+mux.HandleFunc("PUT /queues/{name}",      queueHandler.UpdateQueue)
+mux.HandleFunc("GET /queues/{name}/stats", queueHandler.GetQueueStats)
```

---

## Test stubs (4 files)

`QueueConfigStore` interface methods stubbed in all existing fake stores to satisfy the updated `Store` interface. No new test logic — Phase 8 queue tests live in the new files.

Files updated:
- `internal/api/grpc/server_test.go`
- `internal/api/handler/job_test.go`
- `internal/api/handler/pipeline_test.go`
- `internal/pipeline/advancement_test.go`

Each gets the same 3 stubs:
```go
func (f *fakeStore) ListQueueConfigs(_ context.Context) ([]*store.QueueConfig, error) { return nil, nil }
func (f *fakeStore) GetQueueConfig(_ context.Context, _ string) (*store.QueueConfig, error) { return nil, store.ErrNotFound }
func (f *fakeStore) UpsertQueueConfig(_ context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) { return cfg, nil }
```

---

## New untracked files

| File | Purpose |
|------|---------|
| `internal/scheduler/ratelimiter.go` | Token bucket per queue (`QueueRateLimiter`, `BucketConfig`, `Allow`) |
| `internal/scheduler/fairqueue.go` | Weighted fair queue (`QueueAllocation`, `ComputeAllocations`, `FairQueue.FetchReadyJobs`) |
| `internal/api/handler/queue.go` | HTTP handlers for `/queues` CRUD + stats |
| `internal/store/migrations/003_queue_config.up.sql` | Creates `queue_config` table |
| `internal/store/migrations/003_queue_config.down.sql` | Drops `queue_config` table |
