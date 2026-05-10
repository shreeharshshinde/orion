# ADR-004: Buffered `jobCh` as the Backpressure Boundary

**Date:** 2024-01-20
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

The worker pool has two concurrent layers:
- **Dequeue goroutines** — one per queue, pulling messages from Redis via `XREADGROUP`
- **Worker goroutines** — N goroutines (default 10) executing jobs

These layers need a connection point. The naive approach is a direct call: dequeue goroutine directly calls `executeJob`. But this fails under load: if all worker slots are busy, the dequeue goroutine blocks, and new Redis messages accumulate in the PEL (Pending Entry List) without a consumer. Under extreme load, XAUTOCLAIM reclaims them and redelivers to another worker — causing double execution.

The problem reduces to: **how do we prevent dequeue goroutines from accepting more work than workers can process, without dropping or duplicating messages?**

## Decision

Use a **buffered Go channel (`jobCh`) with capacity equal to worker concurrency** as the backpressure boundary between dequeue goroutines and worker goroutines:

```go
jobCh := make(chan *jobTask, cfg.Concurrency)  // capacity = N workers

// Dequeue goroutine: blocks when jobCh is full
select {
case jobCh <- &jobTask{job: job, ackFn: ackFn}:
    // accepted — a worker will pick it up
case <-ctx.Done():
    ackFn(fmt.Errorf("shutting down"))
    return
}

// Worker goroutine: receives from jobCh
for task := range jobCh {
    executeJob(ctx, task)
}
```

When all N worker slots are busy, the channel is at capacity. The dequeue goroutine's `case jobCh <- task:` blocks. The blocking dequeue goroutine stops calling `XREADGROUP` — Redis messages stay in the stream, not in process memory.

Evaluated options:

| Option | In-process memory risk | Crash safety | Complexity |
|---|---|---|---|
| Unbuffered channel (cap=0) | Low | High | Low — but kills throughput |
| Buffered channel (cap=N) | Bounded | High (unsent = still in Redis PEL) | Low |
| Semaphore + goroutine-per-message | Medium | Medium | High |
| Worker-pulls-from-queue directly | Low | Highest | Medium |

The buffered channel with `cap=N` is the exact right size: it allows the dequeue goroutine to have one message ready for each worker goroutine, eliminating idle time between jobs without allowing unbounded growth.

## Consequences

**Positive:**
- **Crash safety**: messages not yet sent to `jobCh` remain in Redis PEL, delivered to another worker after XAUTOCLAIM timeout (~90s). No messages are lost on crash.
- **Bounded memory**: at most N jobs in process memory simultaneously (N = Concurrency)
- **Natural backpressure**: fast producers are naturally slowed by slow consumers — no explicit rate limiting needed in the dequeue layer
- **Simple**: 5 lines of code implement the pattern correctly

**Negative:**
- `cap=N` means the pool can have up to 2N jobs "claimed" at once: N in `jobCh` + N executing. This is a known and acceptable tradeoff.
- If a worker goroutine panics without recovery, its slot is permanently lost — mitigated by `PanicRecover` decorator in the handler layer

## Implementation Notes

Located in `internal/worker/pool.go`. The `drain()` function on shutdown closes `jobCh` after cancelling the context, allowing worker goroutines to finish processing any tasks already in the channel before exiting — ensuring graceful shutdown completes all in-flight work within `ShutdownTimeout`.