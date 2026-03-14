# Step 05 — Queue Interface (`internal/queue/queue.go`)

## What is this file?

This file defines the **contract for the message queue** — the pipe through which jobs flow from the scheduler to workers.

Like the store interface, this defines *what* the queue can do without saying *how*. The actual Redis implementation comes in Step 07.

## Why a queue at all?

Without a queue, the scheduler would call the worker directly:
```
Scheduler → HTTP call → Worker
```

Problems:
- If the worker is busy, the job is lost
- If the worker crashes mid-call, the job is lost
- You can only have one worker (no horizontal scaling)
- Scheduler and worker must both be running at the same moment

With a queue:
```
Scheduler → Redis Stream → Worker(s)
```

Benefits:
- Jobs persist in Redis even if all workers are down
- Multiple workers read from the same queue (horizontal scaling)
- If a worker crashes, the job stays in Redis and gets redelivered
- Scheduler and workers are **decoupled** — they don't know about each other

## The Queue interface

```go
type Queue interface {
    Enqueue(ctx, job) error
    Dequeue(ctx, queueName, visibilityTimeout) (*Job, AckFunc, error)
    Len(ctx, queueName) (int64, error)
    Dead(ctx, job, reason) error
    Flush(ctx, queueName) error
    Close() error
}
```

### `Enqueue` — putting a job in
The scheduler calls this after it claims a job from PostgreSQL.
```go
err := queue.Enqueue(ctx, job)
// Internally: XADD orion:queue:default * job_id "abc" payload "{...}"
```
If the job has a future `ScheduledAt`, it goes into the scheduled sorted set instead of the stream.

### `Dequeue` — taking a job out
Workers call this in a loop. It **blocks** until a job is available.
```go
job, ackFn, err := queue.Dequeue(ctx, "orion:queue:default", 5*time.Minute)
// Process the job...
ackFn(nil)   // success: remove from queue permanently
ackFn(err)   // failure: leave in queue, will be redelivered
```

### `AckFunc` — the acknowledgment function
```go
type AckFunc func(err error) error
```

This is the most important part of reliable delivery:

- `ackFn(nil)` → **ACK**: "I processed this successfully, remove it"
  - Internally: `XACK stream consumer-group message-id`
- `ackFn(someError)` → **NACK**: "I failed, put it back"
  - Internally: do nothing — message stays in Redis PEL (Pending Entry List)

The PEL is Redis's "delivery journal" — it tracks which messages have been given to consumers but not yet acknowledged. If a worker crashes without calling `ackFn`, the message stays in the PEL and gets redelivered after the visibility timeout.

## Queue names

```go
const (
    QueueDefault   = "orion:queue:default"   // normal priority jobs
    QueueHigh      = "orion:queue:high"       // high priority jobs
    QueueLow       = "orion:queue:low"        // batch/background jobs
    QueueDead      = "orion:queue:dead"       // jobs that exhausted all retries
    QueueScheduled = "orion:queue:scheduled"  // SORTED SET: future-scheduled jobs
)
```

**Why three priority queues?**

Workers check `QueueHigh` first, then `QueueDefault`, then `QueueLow`. If a critical training job comes in while the worker is processing batch jobs, the critical job jumps to the front of the line.

**The Dead queue:**

When a job fails more than `MaxRetries` times, it goes to `QueueDead`. It won't be retried automatically. A human (or admin tool) must inspect why it failed and decide what to do — either fix the root cause and replay it, or discard it.

## The Message envelope

```go
type Message struct {
    ID         string
    JobID      string
    QueueName  string
    Body       []byte    // the serialized Job as JSON
    Attempt    int
    EnqueuedAt time.Time
}
```

When a job enters the queue, it's serialized to JSON and stored in this envelope. When dequeued, it's deserialized back into a `domain.Job`.

## Why Redis Streams specifically?

Redis offers several data structures for queues:

| Option | Problem |
|---|---|
| `RPUSH`/`BLPOP` (Lists) | If worker crashes before processing, message is **lost forever** |
| `SUBSCRIBE`/`PUBLISH` (PubSub) | Not stored at all — if no consumer is listening, message is lost |
| Streams + Consumer Groups | Messages stay in PEL until ACKed — **at-least-once delivery** |

Redis Streams is the only option that survives worker crashes. Full explanation in `docs/adr/ADR-001-queue-design.md`.

## File location
```
orion/
└── internal/
    └── queue/
        ├── queue.go           ← you are here (interface)
        └── redis/
            └── redis_queue.go ← Step 07 (implementation)
```

## Next step
Before implementing the Redis queue, we need the **retry backoff utility** — the math that decides how long to wait before retrying a failed job. That comes next, in `pkg/retry/`.