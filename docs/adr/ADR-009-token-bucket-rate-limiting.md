# ADR-009: Token Bucket for Per-Queue Rate Limiting

**Date:** 2024-03-10
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

Phase 8 introduced per-queue rate limiting to prevent any single queue from overwhelming the scheduler's dispatch bandwidth or the worker pool's execution capacity. The rate limiter sits in the scheduler's dispatch loop: before enqueuing a job to Redis, it checks whether the queue has capacity.

Three rate limiting algorithms were considered:

1. **Fixed window counter** — count requests per N-second window, reset at boundary
2. **Sliding window log** — store timestamp of every request, count those within the window
3. **Token bucket** — tokens accumulate at a fixed rate up to a burst ceiling; each dispatch consumes one token
4. **Leaky bucket** — requests queue and drain at a fixed rate; no burst allowed

The scheduler dispatches jobs in bursts (up to `BatchSize` jobs every 2 seconds). A rate limiter that penalizes bursting would force the scheduler to spread each batch over multiple ticks, adding unnecessary latency to job dispatch.

## Decision

**Use a token bucket rate limiter per queue**, implemented with `golang.org/x/time/rate.Limiter`:

```go
type QueueRateLimiter struct {
    mu      sync.Mutex
    buckets map[string]*rate.Limiter
}

func (rl *QueueRateLimiter) Allow(queueName string) bool {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    limiter, ok := rl.buckets[queueName]
    if !ok {
        return true // unknown queue: no limit applied
    }
    return limiter.Allow()
}
```

Each queue's bucket is configured with:
- **Rate** (`r`): tokens added per second (e.g., 100 tokens/sec for the high queue)
- **Burst** (`b`): maximum token accumulation (e.g., burst=20 means up to 20 jobs can be dispatched instantly)

`golang.org/x/time/rate` implements the token bucket using a virtual clock — no goroutine runs in the background to refill tokens. Instead, tokens are computed lazily on each `Allow()` call based on elapsed time, making it efficient and accurate.

Evaluated options:

| Algorithm | Burst support | Precision | Complexity | Thundering herd |
|---|---|---|---|---|
| Fixed window counter | None | Low (boundary spike) | Low | High |
| Sliding window log | None | High | High (O(N) per check) | Low |
| **Token bucket** | **Yes** | **High** | **Low** | **Low** |
| Leaky bucket | No (strict) | High | Low | None |

Token bucket is the industry standard for this use case (used by AWS API Gateway, Nginx, Envoy, and `golang.org/x/time/rate`). It allows burst absorption while enforcing a sustained rate ceiling — exactly what the scheduler needs.

## Consequences

**Positive:**
- Burst capacity absorbs the natural bursty dispatch pattern of the 2-second scheduler tick without artificial latency
- Rate is enforced over time — a queue cannot sustain more than `RatePerSec` dispatches per second indefinitely
- `golang.org/x/time/rate` is a well-tested standard library extension — no custom algorithm to maintain
- Live configuration reload: `UpdateConfig(queueName, rate, burst)` replaces the limiter atomically under the mutex
- `Available()` method exposes current token count to the `/queues/{name}/stats` API and Prometheus gauge

**Negative:**
- A queue that has been idle accumulates `Burst` tokens. After a long idle period, it can dispatch `Burst` jobs instantly before the rate limit kicks in. This is intentional but operators must set `Burst` conservatively if instant spikes are undesirable.
- The rate limiter is in-process and per-scheduler-instance. Since Orion uses leader election (only one scheduler active at a time), this is correct — there is exactly one rate limiter governing dispatch at any moment.

## Configuration

```bash
# Per-queue token bucket configuration
ORION_QUEUE_HIGH_RATE_PER_SEC=100.0   # sustain 100 dispatches/sec
ORION_QUEUE_HIGH_BURST=20             # allow up to 20 instant dispatches

ORION_QUEUE_DEFAULT_RATE_PER_SEC=50.0
ORION_QUEUE_DEFAULT_BURST=10

ORION_QUEUE_LOW_RATE_PER_SEC=10.0
ORION_QUEUE_LOW_BURST=5
```

Dynamic updates via `PUT /queues/{name}` write to the `queue_config` PostgreSQL table and are picked up by the scheduler on its next tick (within 2 seconds). No restart required.

## Implementation Notes

Located in `internal/scheduler/ratelimiter.go`. The `QueueRateLimiter` is constructed in `cmd/scheduler/main.go` and passed to `scheduler.New()`. The `sync.Mutex` protects the `buckets` map for concurrent `Allow()` and `UpdateConfig()` calls — though in practice, the scheduler's dispatch loop is single-threaded so contention is minimal.

## Related Decisions

- ADR-001: Redis Streams queue design — the rate limiter governs how fast jobs enter Redis, complementing the at-least-once delivery guarantee
- ADR-010: Weighted fair scheduling — the rate limiter and fair scheduler work in tandem; the fair scheduler determines **how many** jobs per queue, the rate limiter determines **how fast**