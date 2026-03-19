# Step 06 — Retry Backoff (`pkg/retry/retry.go`)

## What is this file?

This file contains the **math for deciding how long to wait before retrying a failed job**.

When a job fails (network error, out-of-memory, temporary service unavailability), we don't retry it immediately — we wait a bit. But how long? This file answers that question.

## Why not just `time.Sleep(5 * time.Second)` for every retry?

Fixed delays cause the "thundering herd" problem:

```
10:00:00 - 100 jobs all fail at the same moment
10:00:05 - ALL 100 retry at exactly the same time → system overwhelmed → all fail again
10:00:10 - ALL 100 retry again → overwhelmed again
           (this repeats until you fix the underlying issue)
```

A system under stress gets hit by synchronized retry waves, making recovery impossible.

## The solution: Full-Jitter Exponential Backoff

```
Formula: random(0, min(cap, base × 2^attempt))
```

Step by step for `base=5s, cap=30min`:

| Attempt | Ceiling | Actual wait (random) |
|---|---|---|
| 0 | 10s | might be 3s, 7s, 1s... |
| 1 | 20s | might be 14s, 4s, 19s... |
| 2 | 40s → capped at 30min | might be 22s, 38s... |
| 5+ | 30min (capped) | might be 12min, 28min... |

Each job picks its own random wait time. 100 failing jobs scatter across the window:
```
10:00:00 - 100 jobs fail
10:00:03 - job A retries
10:00:07 - job B retries
10:00:11 - job C retries
... spread out over the window ...
```

The system gets a chance to recover between retries.

## Why "full" jitter vs "equal" jitter?

```go
// Full jitter: range is [0, ceiling] — can be very small
// Good for: most retry scenarios
FullJitterBackoff(attempt, 5*time.Second, 30*time.Minute)

// Equal jitter: range is [ceiling/2, ceiling] — guaranteed minimum wait
// Good for: when you need at least some delay (e.g., rate-limited APIs)
EqualJitterBackoff(attempt, 5*time.Second, 30*time.Minute)
```

Orion uses `FullJitterBackoff` for ML job retries because:
- ML jobs can fail for transient reasons (node restart, temp network blip)
- We want some retries to happen quickly in case the issue is already resolved
- The random spread prevents synchronized retries

## Where is this used?

In `internal/worker/pool.go`, the `nextRetryTime()` method:
```go
func (p *Pool) nextRetryTime(job *domain.Job) *time.Time {
    if !job.IsRetryable() {
        return nil
    }
    // Use the job's current attempt count to calculate backoff
    delay := retry.FullJitterBackoff(job.Attempt, 5*time.Second, 30*time.Minute)
    t := time.Now().Add(delay)
    return &t
}
```

The `next_retry_at` timestamp is stored in PostgreSQL. The scheduler's retry-promotion loop checks `WHERE next_retry_at <= NOW()` to find jobs ready to retry.

## The `WithRetry` helper

```go
err := retry.WithRetry(ctx, 3, retry.FullJitterBackoff, func() error {
    return sendHTTPRequest()
})
```

This wraps any function with automatic retry + backoff. Used internally for transient operations like connecting to Redis or PostgreSQL at startup.

## Why is this in `pkg/` not `internal/`?

```
orion/
├── internal/  ← only Orion's own code can use this
└── pkg/       ← any external project can import this
    └── retry/
        └── retry.go   ← you are here
```

The retry package is genuinely reusable. Any Go service that needs retry logic could import `github.com/shreeharsh-a/orion/pkg/retry`. Putting it in `pkg/` signals this intent.

## Next step
Now we implement the **Redis queue** — the actual code that talks to Redis Streams using XADD, XREADGROUP, and XAUTOCLAIM.