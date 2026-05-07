# Rate Limiter — Phase 8

## What This Document Covers

The token bucket rate limiter added in Phase 8: the algorithm, the implementation in `internal/scheduler/ratelimiter.go`, why specific design choices were made, and how to observe it in production.

---

## The Problem It Solves

Without rate limiting, the scheduler dispatches as many jobs as possible on every tick. If a queue has 1000 jobs ready, all 1000 get dispatched in the first tick (up to `batchSize`). This creates two problems:

1. **Redis stream flooding** — 1000 `XADD` calls in 2 seconds overwhelms the stream consumer
2. **No throttle knob** — operators cannot slow down a runaway queue without stopping the scheduler

The token bucket gives operators a per-queue throttle: `rate_per_sec` controls the sustained dispatch rate, `burst` controls how many jobs can dispatch instantly before throttling kicks in.

---

## Token Bucket Algorithm

```
State per queue:
  tokens     float64   — current token count (starts at burst)
  maxTokens  float64   — burst capacity ceiling
  refillRate float64   — tokens added per second
  lastRefill time.Time — timestamp of last refill

On Allow(queueName):
  elapsed = now - lastRefill
  tokens  = min(maxTokens, tokens + elapsed × refillRate)
  lastRefill = now

  if tokens < 1.0:
    return false   // rate limited

  tokens -= 1.0
  return true      // dispatch allowed
```

### Why it starts full

All buckets initialize with `tokens = burst`. This means the first batch of jobs dispatches immediately without waiting for tokens to accumulate. An empty bucket at startup would artificially delay the first dispatch by `burst / rate_per_sec` seconds — wrong behavior for a system that just restarted.

### Why `float64` for tokens

At `rate_per_sec = 0.5` (one job every 2 seconds) with a 2-second scheduler tick:
- Integer arithmetic: `floor(2s × 0.5) = 1` token added per tick — correct but coarse
- At `rate_per_sec = 0.1` (one job every 10 seconds): `floor(2s × 0.1) = 0` tokens — **never fires**

`float64` accumulates fractional tokens: `2s × 0.1 = 0.2` tokens per tick. After 5 ticks (10 seconds), `tokens = 1.0` and one job dispatches. Correct at any rate.

### Lazy refill, not a background goroutine

Tokens are refilled inside `Allow()` based on `time.Since(lastRefill)`. No background goroutine runs a ticker. This is simpler (no goroutine lifecycle to manage), accurate (refill is proportional to actual elapsed time), and has no goroutine leak risk.

---

## Implementation

```go
// internal/scheduler/ratelimiter.go

type BucketConfig struct {
    RatePerSec float64
    Burst      int
}

type QueueRateLimiter struct {
    mu      sync.Mutex
    buckets map[string]*tokenBucket
}

type tokenBucket struct {
    tokens     float64
    maxTokens  float64
    refillRate float64
    lastRefill time.Time
}
```

### `Allow(queueName string) bool`

The hot path — called once per job per scheduler tick. Acquires the mutex, refills, checks, and consumes one token atomically.

Unknown queue names return `true` (no limit applied). This means adding a new queue to Redis without a corresponding `queue_config` row does not block dispatch — it just runs unlimited until configured.

### `UpdateConfig(queueName string, ratePerSec float64, burst int)`

Called by `PUT /queues/{name}` after writing to PostgreSQL. Updates the in-process bucket immediately. If `burst` decreased, current tokens are capped to the new maximum. If the queue doesn't exist yet, a new full bucket is created.

### `Available(queueName string) float64`

Returns the current token count without consuming. Used by `GET /queues/{name}/stats` to report `rate_tokens_avail`. Note: this is a snapshot — the value may change between the read and the next `Allow()` call.

---

## Wiring

### In `cmd/scheduler/main.go`

```go
rateLimiter := scheduler.NewQueueRateLimiter(map[string]scheduler.BucketConfig{
    "orion:queue:high":    {RatePerSec: cfg.Queue.High.RatePerSec,    Burst: cfg.Queue.High.Burst},
    "orion:queue:default": {RatePerSec: cfg.Queue.Default.RatePerSec, Burst: cfg.Queue.Default.Burst},
    "orion:queue:low":     {RatePerSec: cfg.Queue.Low.RatePerSec,     Burst: cfg.Queue.Low.Burst},
})
```

The scheduler calls `rateLimiter.Allow(job.QueueName)` for each candidate job in `scheduleQueuedJobs`. Jobs that return `false` are skipped this tick and remain in `queued` status — they will be picked up on the next tick when tokens have refilled.

### In `cmd/api/main.go`

The same `QueueRateLimiter` instance is passed to `NewQueueHandler`. This allows `PUT /queues/{name}` to call `rateLimiter.UpdateConfig(...)` and apply changes immediately to the running scheduler without a restart.

Note: the API server and scheduler are separate processes. The API's `rateLimiter` instance is used only for `GET /queues/{name}/stats` reporting. The scheduler's `rateLimiter` is the one that actually gates dispatch. Both are updated when `PUT /queues/{name}` is called — the API's immediately, the scheduler's on its next DB reload tick (≤2 seconds).

---

## Observability

### Prometheus counter

`orion_queue_rate_limited_total{queue="orion:queue:low"}` — incremented each time `Allow()` returns `false` for a job. Visible in Grafana.

A sustained non-zero rate means the queue is consistently hitting its limit. Possible responses:
- Increase `rate_per_sec` if the limit is too conservative
- Leave it — the limit is working as intended to protect downstream systems
- Investigate why so many jobs are being submitted to this queue

### Stats endpoint

```bash
curl http://localhost:8080/queues/orion:queue:high/stats
# {
#   "queue_name": "orion:queue:high",
#   "depth": 0,
#   "rate_tokens_avail": 20.0   ← full bucket, no recent dispatch
# }

# After submitting 15 high-priority jobs:
# {
#   "rate_tokens_avail": 5.0    ← 15 tokens consumed, 5 remaining
# }
```

---

## Tuning Guide

| Scenario | Adjustment |
|---|---|
| High-priority jobs delayed at startup | Increase `burst` — allows more jobs to dispatch instantly |
| Low queue flooding Redis | Decrease `rate_per_sec` |
| Queue never dispatches | Check `enabled=true` in `queue_config`; check `rate_per_sec > 0` |
| Rate limiting too aggressive | Increase `rate_per_sec` or `burst` via `PUT /queues/{name}` |
| Need to pause a queue temporarily | `PUT /queues/{name}` with `{"enabled": false}` |

Live changes take effect within one scheduler tick (default 2 seconds). No restart required.
